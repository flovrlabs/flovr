// Package server runs workflows when their triggers fire: an HTTP request to
// a webhook path, or a cron schedule. It also serves a read-only web page
// showing the workflows and their recent runs.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"aia/internal/engine"
	"aia/internal/workflow"
)

const (
	maxBody    = 1 << 20
	runTimeout = 5 * time.Minute
	running    = "running"
)

type Options struct {
	Model engine.Model
	HTTP  *http.Client
	Env   map[string]string // what expressions see as env

	// Token, if set, must accompany every request: webhooks as
	// "Authorization: Bearer <token>", the web page as the password of HTTP
	// basic authentication. Without a token the server answers only requests
	// addressed to this machine by a local name.
	Token string

	// Commit identifies the version of the workflow files; it is recorded
	// with every run.
	Commit string

	// History is a file that finished runs are appended to and that the
	// server reads back when it starts. Empty keeps runs in memory only.
	History string

	Log   func(format string, args ...any) // one line per event; nil discards
	OnRun func(Run)                        // called when a run finishes, if set
}

// Run is the record of one execution of a workflow.
type Run struct {
	ID       string    `json:"run"`
	Workflow string    `json:"workflow"`
	Source   string    `json:"source"` // what started it, such as "webhook /tickets"
	Commit   string    `json:"commit,omitempty"`
	Started  time.Time `json:"started"`
	Millis   int64     `json:"ms"`
	Status   string    `json:"status"` // "running", "ok" or "failed"
	Error    string    `json:"error,omitempty"`
	Steps    []Step    `json:"steps"`
	Trigger  any       `json:"trigger,omitempty"`
}

type Step struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Millis int64  `json:"ms"`
	Output any    `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

type Server struct {
	opts      Options
	workflows []*workflow.Workflow
	webhooks  map[string]*workflow.Workflow // by request path
	scheduled []*workflow.Workflow
	history   *history
	running   sync.WaitGroup
}

// New prepares to serve the workflows that have a webhook or cron trigger. It
// refuses a set of workflows that could not run correctly: two that share a
// webhook path, one whose path the server uses itself, or any that read an
// environment variable that is not set.
func New(workflows []*workflow.Workflow, opts Options) (*Server, error) {
	if opts.Log == nil {
		opts.Log = func(string, ...any) {}
	}
	s := &Server{opts: opts, workflows: workflows, webhooks: map[string]*workflow.Workflow{}}

	var unset []string
	for _, w := range workflows {
		switch t := w.Trigger; {
		case t != nil && t.Webhook != "":
			if reserved(t.Webhook) {
				return nil, fmt.Errorf("%s: the webhook path %s is used by the server itself", w.Path, t.Webhook)
			}
			if other, taken := s.webhooks[t.Webhook]; taken {
				return nil, fmt.Errorf("%s and %s both use the webhook path %s", other.Path, w.Path, t.Webhook)
			}
			s.webhooks[t.Webhook] = w
		case t != nil && t.Cron != "":
			s.scheduled = append(s.scheduled, w)
		default:
			continue
		}
		for _, name := range w.EnvVars() {
			if opts.Env[name] == "" && !slices.Contains(unset, name) {
				unset = append(unset, name)
			}
		}
	}
	if len(s.webhooks)+len(s.scheduled) == 0 {
		return nil, errors.New("none of the workflows has a webhook or cron trigger, so there is nothing to serve")
	}
	if len(unset) > 0 {
		slices.Sort(unset)
		return nil, fmt.Errorf("the workflows read environment variables that are not set: %s", strings.Join(unset, ", "))
	}

	h, err := openHistory(opts.History)
	if err != nil {
		return nil, fmt.Errorf("run history: %w", err)
	}
	s.history = h
	return s, nil
}

// reserved reports whether the server answers path itself.
func reserved(path string) bool {
	return path == "/" || path == "/healthz" || strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/ui/")
}

// Close releases the run history file.
func (s *Server) Close() { s.history.close() }

// Routes describes what the server responds to, one line per trigger.
func (s *Server) Routes() []string {
	var lines []string
	for path, w := range s.webhooks {
		lines = append(lines, fmt.Sprintf("POST %s  ->  %s  (%s)", path, w.Name, w.Path))
	}
	slices.Sort(lines)
	for _, w := range s.scheduled {
		lines = append(lines, fmt.Sprintf("cron %s  ->  %s  (%s)", w.Trigger.Cron, w.Name, w.Path))
	}
	return lines
}

// Start begins running the scheduled workflows. The function it returns stops
// the schedule and waits for scheduled runs in progress.
func (s *Server) Start() (stop func()) {
	c := cron.New()
	for _, w := range s.scheduled {
		// The schedule was checked when the workflow was loaded.
		c.AddFunc(w.Trigger.Cron, func() {
			s.run(newID(), w, map[string]any{"time": time.Now().Format(time.RFC3339)}, "cron "+w.Trigger.Cron)
		})
	}
	c.Start()
	return func() { <-c.Stop().Done() }
}

// Wait blocks until the runs started by webhooks have finished.
func (s *Server) Wait() { s.running.Wait() }

func (s *Server) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		rw.WriteHeader(http.StatusOK)
		return
	}
	// Without a token, nothing but the address protects the server. Checking
	// the Host header keeps a web page on another site from reaching it by
	// pointing its own domain name at this machine.
	if s.opts.Token == "" && !localName(r.Host) {
		reply(rw, http.StatusForbidden, map[string]string{"error": "without AIA_TOKEN this server only answers requests addressed to localhost"})
		return
	}
	if w, ok := s.webhooks[r.URL.Path]; ok {
		s.webhook(rw, r, w)
		return
	}
	s.page(rw, r)
}

// webhook starts w with the request's JSON body as trigger.body. It answers
// 202 at once and runs the workflow in the background, unless the request has
// ?wait=true, in which case it answers with the finished run.
func (s *Server) webhook(rw http.ResponseWriter, r *http.Request, w *workflow.Workflow) {
	if r.Method != http.MethodPost {
		rw.Header().Set("Allow", http.MethodPost)
		reply(rw, http.StatusMethodNotAllowed, map[string]string{"error": "send a POST request"})
		return
	}
	if s.opts.Token != "" {
		given := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(given), []byte(s.opts.Token)) != 1 {
			reply(rw, http.StatusUnauthorized, map[string]string{"error": "missing or wrong bearer token"})
			return
		}
	}
	// A web page on another site can make a browser send a form, but not
	// JSON, so this keeps such a page from starting runs.
	if kind, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); kind != "application/json" {
		reply(rw, http.StatusUnsupportedMediaType, map[string]string{"error": "send the body with Content-Type: application/json"})
		return
	}

	data, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, maxBody))
	if err != nil {
		reply(rw, http.StatusRequestEntityTooLarge, map[string]string{"error": "the request body is larger than 1 MB"})
		return
	}
	var body any
	if err := json.Unmarshal(data, &body); err != nil {
		reply(rw, http.StatusBadRequest, map[string]string{"error": "the request body must be JSON"})
		return
	}
	trigger := map[string]any{"body": body}
	source := "webhook " + r.URL.Path
	id := newID()

	if r.URL.Query().Get("wait") == "true" {
		run := s.run(id, w, trigger, source)
		status := http.StatusOK
		if run.Status != "ok" {
			status = http.StatusInternalServerError
		}
		reply(rw, status, run)
		return
	}
	s.running.Add(1)
	go func() {
		defer s.running.Done()
		s.run(id, w, trigger, source)
	}()
	reply(rw, http.StatusAccepted, map[string]string{"run": id})
}

// run executes the workflow to completion. It is not tied to the request that
// started it, so a caller that disconnects does not cancel the run.
func (s *Server) run(id string, w *workflow.Workflow, trigger any, source string) Run {
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()

	run := Run{
		ID: id, Workflow: w.Name, Source: source, Commit: s.opts.Commit,
		Started: time.Now(), Status: running, Steps: []Step{}, Trigger: trigger,
	}
	s.history.put(run)
	s.opts.Log("%s %s started by %s", id, w.Name, source)

	eng := &engine.Engine{
		Model: s.opts.Model,
		HTTP:  s.opts.HTTP,
		Env:   s.opts.Env,
		OnStep: func(r engine.StepResult) {
			step := Step{
				ID: r.ID, Kind: r.Kind, Status: string(r.Status), Detail: r.Detail,
				Millis: r.Duration.Milliseconds(), Output: r.Output,
			}
			line := fmt.Sprintf("%s   %s %s", id, r.ID, r.Status)
			if r.Detail != "" {
				line += "  " + r.Detail
			}
			if r.Err != nil {
				step.Error = r.Err.Error()
				line += "  " + firstLine(step.Error)
			}
			run.Steps = append(run.Steps, step)
			s.history.put(run)
			// Step outputs can hold customer data, so they are not logged.
			s.opts.Log("%s", line)
		},
	}
	err := eng.Run(ctx, w, trigger)
	run.Status = "ok"
	if err != nil {
		run.Status, run.Error = "failed", err.Error()
	}
	run.Millis = time.Since(run.Started).Milliseconds()
	s.history.put(run)
	s.opts.Log("%s %s %s in %s", id, w.Name, run.Status, time.Duration(run.Millis)*time.Millisecond)

	if s.opts.OnRun != nil {
		s.opts.OnRun(run)
	}
	return run
}

// localName reports whether a Host header names this machine itself.
func localName(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func newID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return "r-" + hex.EncodeToString(b)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func reply(rw http.ResponseWriter, status int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	json.NewEncoder(rw).Encode(v)
}

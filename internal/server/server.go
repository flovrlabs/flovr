// Package server runs workflows when their triggers fire: an HTTP request to
// a webhook path, or a cron schedule.
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
)

type Options struct {
	Model engine.Model
	HTTP  *http.Client
	Env   map[string]string // what expressions see as env

	// Token, if set, must accompany every webhook request as
	// "Authorization: Bearer <token>".
	Token string

	Log   func(format string, args ...any) // one line per event; nil discards
	OnRun func(Run)                        // called when a run finishes, if set
}

// Run is the record of one execution of a workflow.
type Run struct {
	ID       string `json:"run"`
	Workflow string `json:"workflow"`
	Status   string `json:"status"` // "ok" or "failed"
	Error    string `json:"error,omitempty"`
	Steps    []Step `json:"steps"`
}

type Step struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Output any    `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

type Server struct {
	opts      Options
	webhooks  map[string]*workflow.Workflow // by request path
	scheduled []*workflow.Workflow
	running   sync.WaitGroup
}

// New prepares to serve the workflows that have a webhook or cron trigger. It
// refuses a set of workflows that could not run correctly: two that share a
// webhook path, or any that read an environment variable that is not set.
func New(workflows []*workflow.Workflow, opts Options) (*Server, error) {
	if opts.Log == nil {
		opts.Log = func(string, ...any) {}
	}
	s := &Server{opts: opts, webhooks: map[string]*workflow.Workflow{}}

	var unset []string
	for _, w := range workflows {
		switch t := w.Trigger; {
		case t != nil && t.Webhook != "":
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
	return s, nil
}

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
			s.run(w, map[string]any{"time": time.Now().Format(time.RFC3339)}, "cron "+w.Trigger.Cron)
		})
	}
	c.Start()
	return func() { <-c.Stop().Done() }
}

// Wait blocks until the runs started by webhooks have finished.
func (s *Server) Wait() { s.running.Wait() }

// ServeHTTP starts the workflow whose webhook path is requested, with the
// request's JSON body as trigger.body. It answers 202 at once and runs the
// workflow in the background, unless the request has ?wait=true, in which case
// it answers with the finished run.
func (s *Server) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		rw.WriteHeader(http.StatusOK)
		return
	}
	w, ok := s.webhooks[r.URL.Path]
	if !ok {
		reply(rw, http.StatusNotFound, map[string]string{"error": "no workflow has this webhook path"})
		return
	}
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

	if r.URL.Query().Get("wait") == "true" {
		run := s.run(w, trigger, source)
		status := http.StatusOK
		if run.Status != "ok" {
			status = http.StatusInternalServerError
		}
		reply(rw, status, run)
		return
	}
	id := newID()
	s.running.Add(1)
	go func() {
		defer s.running.Done()
		s.runAs(id, w, trigger, source)
	}()
	reply(rw, http.StatusAccepted, map[string]string{"run": id})
}

func (s *Server) run(w *workflow.Workflow, trigger any, source string) Run {
	return s.runAs(newID(), w, trigger, source)
}

// runAs executes the workflow to completion. It is not tied to the request
// that started it, so a caller that disconnects does not cancel the run.
func (s *Server) runAs(id string, w *workflow.Workflow, trigger any, source string) Run {
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()

	run := Run{ID: id, Workflow: w.Name, Status: "ok"}
	start := time.Now()
	s.opts.Log("%s %s started by %s", id, w.Name, source)

	eng := &engine.Engine{
		Model: s.opts.Model,
		HTTP:  s.opts.HTTP,
		Env:   s.opts.Env,
		OnStep: func(r engine.StepResult) {
			step := Step{ID: r.ID, Kind: r.Kind, Status: string(r.Status), Output: r.Output}
			line := fmt.Sprintf("%s   %s %s", id, r.ID, r.Status)
			if r.Detail != "" {
				line += "  " + r.Detail
			}
			if r.Err != nil {
				step.Error = r.Err.Error()
				line += "  " + firstLine(step.Error)
			}
			run.Steps = append(run.Steps, step)
			// Step outputs can hold customer data, so they are not logged.
			s.opts.Log("%s", line)
		},
	}
	if err := eng.Run(ctx, w, trigger); err != nil {
		run.Status, run.Error = "failed", err.Error()
	}
	s.opts.Log("%s %s %s in %s", id, w.Name, run.Status, time.Since(start).Round(time.Millisecond))

	if s.opts.OnRun != nil {
		s.opts.OnRun(run)
	}
	return run
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

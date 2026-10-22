// Package server runs workflows when their triggers fire: an HTTP request to
// a webhook path, or a cron schedule. It also serves a web page showing the
// workflows and their recent runs, which can change the workflow files if the
// server is started for editing.
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
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/robfig/cron/v3"

	"github.com/flovrlabs/flovr/internal/engine"
	"github.com/flovrlabs/flovr/internal/workflow"
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

	// Token, if set, must accompany every webhook request as
	// "Authorization: Bearer <token>". Without a token the server answers
	// only requests addressed to this machine by a local name. The token is
	// for whatever sends webhooks, so it does not open the page.
	Token string

	// PageKey, if set, must accompany every request for the page and its
	// API: once as ?key=<key> in the page's address, after which the browser
	// keeps it as a cookie, or as "Authorization: Bearer <key>". It keeps
	// other programs on the machine, and anyone who has only the token, from
	// reading runs and changing files.
	PageKey string

	// MaxRuns is how many runs webhooks may have going at once. A request
	// beyond that is answered 429. Zero means no limit.
	MaxRuns int

	// Commit identifies the version of the workflow files; it is recorded
	// with every run. CommitNow, if set, is asked instead, for servers whose
	// files change while they run.
	Commit    string
	CommitNow func() string

	// Edit lets the web page change the workflow files and the prompt files
	// they name. The server then also takes up changes made to the workflow
	// files by other means, such as an editor or git.
	Edit bool

	// Replay says that Model answers with recorded answers instead of calling
	// the models, which the page then says.
	Replay bool

	// Roots are the folders the server was started with. The page, if it
	// may change files, makes a new workflow in one of them, and can add a
	// step from any component in them.
	Roots []string

	// History is a file that finished runs are appended to and that the
	// server reads back when it starts. Empty keeps runs in memory only.
	History string

	// Market is where the list of components anyone may take from git is
	// read, for the page to offer: an https address, or a file on this
	// machine. Empty offers none.
	Market string

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

	// Tokens is what the run's models read and wrote, over all its steps.
	Tokens engine.Tokens `json:"tokens,omitzero"`

	// Response is what the caller was answered with, for a workflow that
	// has respond.
	Response *engine.Response `json:"response,omitempty"`

	// For a run that is one of the workflow's tests, started from the page:
	// the test's name and what it expected, each with how the run failed to
	// meet it, if it did. Such a run sends none of its http steps.
	Test   string         `json:"test,omitempty"`
	Checks []engine.Check `json:"checks,omitempty"`

	// Gave is what a component gave back, in a run that is one of a
	// component's tests. Workflow then holds the name the page knows the
	// component by.
	Gave any `json:"gave,omitempty"`
}

type Step struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Millis int64  `json:"ms"`
	Output any    `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
	Tries  int    `json:"tries,omitempty"` // only when it ran more than once

	Tokens engine.Tokens `json:"tokens,omitzero"`

	// Answer is what a model wrote when the step could not use it, as it
	// wrote it, so that it can be read beside the error.
	Answer string `json:"answer,omitempty"`

	// Steps is what the steps of a component made of steps did, for a step
	// that uses one.
	Steps []Step `json:"steps,omitempty"`
}

// tokensOf is what a log line says of the tokens of a run, if it used any.
func tokensOf(t engine.Tokens) string {
	if t == (engine.Tokens{}) {
		return ""
	}
	return ", " + t.String()
}

// maxAnswer bounds the answer a run keeps of a model it could not use.
const maxAnswer = 64 << 10

// clip cuts s to at most n bytes, at the start of a character.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// record turns what a step did into the form runs are kept in.
func record(r engine.StepResult) Step {
	step := Step{
		ID: r.ID, Kind: r.Kind, Status: string(r.Status), Detail: r.Detail,
		Millis: r.Duration.Milliseconds(), Output: r.Output, Tokens: r.Tokens,
	}
	if r.Err != nil {
		step.Error = r.Err.Error()
	}
	if r.Tries > 1 {
		step.Tries = r.Tries
	}
	var invalid *engine.InvalidAnswerError
	if errors.As(r.Err, &invalid) {
		step.Answer = clip(invalid.Raw, maxAnswer)
	}
	for _, inner := range r.Steps {
		step.Steps = append(step.Steps, record(inner))
	}
	return step
}

// withoutOutputs returns the steps without what they put out, which may be
// large and may hold what a list should not show.
func withoutOutputs(steps []Step) []Step {
	out := slices.Clone(steps)
	for i := range out {
		out[i].Output = nil
		out[i].Steps = withoutOutputs(out[i].Steps)
	}
	return out
}

type Server struct {
	opts    Options
	live    atomic.Pointer[routing] // what is being served now
	history *history
	running sync.WaitGroup

	// mu is held while the workflows being served, their files or the
	// schedule are changed.
	mu      sync.Mutex
	sources map[string]*source // by workflow path; only when editing
	cron    *cron.Cron
	started bool

	version struct {
		sync.Mutex
		commit string
		asked  time.Time
	}

	slots chan struct{} // one for each run webhooks have going, if MaxRuns is set

	// refusals counts requests turned away for a wrong token or key since
	// the last time the log said so.
	refusals struct {
		sync.Mutex
		count  int
		logged time.Time
	}

	// found is what the last look through Options.Roots turned up.
	found struct {
		sync.Mutex
		files []string // the component files
		at    time.Time
	}

	// marketRead is the market as last read, and when.
	marketRead struct {
		sync.Mutex
		view marketView
		at   time.Time
	}
}

// routing is one consistent set of workflows and how each is reached.
type routing struct {
	workflows []*workflow.Workflow
	webhooks  map[string]*workflow.Workflow // by request path
	scheduled []*workflow.Workflow
}

// New prepares to serve the workflows that have a webhook or cron trigger. It
// refuses a set of workflows that could not run correctly: two that share a
// name or a webhook path, one whose path the server uses itself, or any that
// read an environment variable that is not set.
func New(workflows []*workflow.Workflow, opts Options) (*Server, error) {
	if opts.Log == nil {
		opts.Log = func(string, ...any) {}
	}
	s := &Server{opts: opts, sources: map[string]*source{}}
	if opts.MaxRuns > 0 {
		s.slots = make(chan struct{}, opts.MaxRuns)
	}
	if opts.Edit {
		// The page changes a file only if it still holds what is being
		// served, so both must come from one reading of the file.
		workflows = slices.Clone(workflows)
		for i, w := range workflows {
			fresh, src, err := read(w.Path)
			if err != nil {
				return nil, err
			}
			workflows[i], s.sources[w.Path] = fresh, src
		}
	}
	r, err := route(workflows, opts.Env)
	if err != nil {
		return nil, err
	}
	s.live.Store(r)

	h, err := openHistory(opts.History)
	if err != nil {
		return nil, fmt.Errorf("run history: %w", err)
	}
	s.history = h
	return s, nil
}

// route works out how the workflows are reached, or why they cannot be served
// together.
func route(workflows []*workflow.Workflow, env map[string]string) (*routing, error) {
	r := &routing{workflows: workflows, webhooks: map[string]*workflow.Workflow{}}
	named := map[string]*workflow.Workflow{}
	var unset []string
	for _, w := range workflows {
		if other, taken := named[w.Name]; taken {
			return nil, fmt.Errorf("%s and %s are both named %s", other.Path, w.Path, w.Name)
		}
		named[w.Name] = w

		switch t := w.Trigger; {
		case t != nil && t.Webhook != "":
			if reserved(t.Webhook) {
				return nil, fmt.Errorf("%s: the webhook path %s is used by the server itself", w.Path, t.Webhook)
			}
			if other, taken := r.webhooks[t.Webhook]; taken {
				return nil, fmt.Errorf("%s and %s both use the webhook path %s", other.Path, w.Path, t.Webhook)
			}
			r.webhooks[t.Webhook] = w
		case t != nil && t.Cron != "":
			r.scheduled = append(r.scheduled, w)
		default:
			continue
		}
		for _, name := range w.EnvVars() {
			if env[name] == "" && !slices.Contains(unset, name) {
				unset = append(unset, name)
			}
		}
	}
	if len(r.webhooks)+len(r.scheduled) == 0 {
		return nil, errors.New("none of the workflows has a webhook or cron trigger, so there is nothing to serve")
	}
	if len(unset) > 0 {
		slices.Sort(unset)
		return nil, fmt.Errorf("the workflows read environment variables that are not set: %s", strings.Join(unset, ", "))
	}
	return r, nil
}

// reserved reports whether the server answers path itself.
func reserved(path string) bool {
	return path == "/" || path == "/healthz" || strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/ui/")
}

// Close releases the run history file.
func (s *Server) Close() { s.history.close() }

// Routes describes what the server responds to, one line per trigger.
func (s *Server) Routes() []string {
	live := s.live.Load()
	var lines []string
	for path, w := range live.webhooks {
		lines = append(lines, fmt.Sprintf("POST %s  ->  %s  (%s)", path, w.Name, w.Path))
	}
	slices.Sort(lines)
	for _, w := range live.scheduled {
		lines = append(lines, fmt.Sprintf("cron %s  ->  %s  (%s)", w.Trigger.Cron, w.Name, w.Path))
	}
	return lines
}

// Start begins running the scheduled workflows. The function it returns stops
// the schedule and waits for the runs it started that are in progress.
func (s *Server) Start() (stop func()) {
	s.mu.Lock()
	s.started = true
	s.schedule()
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		c := s.cron
		s.cron, s.started = nil, false
		s.mu.Unlock()
		if c != nil {
			<-c.Stop().Done()
		}
	}
}

// schedule replaces the cron schedule with that of the workflows now being
// served. Runs an earlier schedule started carry on. The caller holds s.mu.
func (s *Server) schedule() {
	if !s.started {
		return
	}
	if s.cron != nil {
		s.cron.Stop()
	}
	s.cron = cron.New()
	for _, w := range s.live.Load().scheduled {
		// The schedule was checked when the workflow was loaded.
		s.cron.AddFunc(w.Trigger.Cron, func() {
			s.running.Add(1)
			defer s.running.Done()
			s.run(newID(), w, map[string]any{"time": time.Now().Format(time.RFC3339)}, "cron "+w.Trigger.Cron)
		})
	}
	s.cron.Start()
}

// Wait blocks until the runs started by webhooks and schedules have finished.
func (s *Server) Wait() { s.running.Wait() }

// commit identifies the version of the workflow files as they are now.
func (s *Server) commit() string {
	if s.opts.CommitNow == nil {
		return s.opts.Commit
	}
	// The page asks every few seconds, and the answer takes running git.
	v := &s.version
	v.Lock()
	defer v.Unlock()
	if time.Since(v.asked) > 5*time.Second {
		v.commit, v.asked = s.opts.CommitNow(), time.Now()
	}
	return v.commit
}

func (s *Server) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz":
		rw.WriteHeader(http.StatusOK)
		return
	case "/favicon.ico":
		// Browsers ask for it on their own, without the page's key. The
		// page names its icon below instead.
		rw.WriteHeader(http.StatusNoContent)
		return
	case "/favicon.svg":
		// The icon is the same for everyone, so it needs no key either.
		h := rw.Header()
		h.Set("Content-Security-Policy", "default-src 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "max-age=86400")
		s.file(rw, "ui/favicon.svg", "image/svg+xml")
		return
	}
	// Without a token, nothing but the address protects the server. Checking
	// the Host header keeps a web page on another site from reaching it by
	// pointing its own domain name at this machine.
	if s.opts.Token == "" && !localName(r.Host) {
		reply(rw, http.StatusForbidden, map[string]string{"error": "without FLOVR_TOKEN this server only answers requests addressed to localhost"})
		return
	}
	if w, ok := s.live.Load().webhooks[r.URL.Path]; ok {
		s.webhook(rw, r, w)
		return
	}
	s.page(rw, r)
}

// webhook starts w with the request's JSON body as trigger.body. It answers
// 202 at once and runs the workflow in the background, unless the request has
// ?wait=true, in which case it answers when the run has finished. A workflow
// that has respond is an endpoint: the request waits and gets that answer.
func (s *Server) webhook(rw http.ResponseWriter, r *http.Request, w *workflow.Workflow) {
	if r.Method != http.MethodPost {
		rw.Header().Set("Allow", http.MethodPost)
		reply(rw, http.StatusMethodNotAllowed, map[string]string{"error": "send a POST request"})
		return
	}
	if s.opts.Token != "" {
		if given, _ := bearer(r); subtle.ConstantTimeCompare([]byte(given), []byte(s.opts.Token)) != 1 {
			s.refused(r)
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
	// A body the workflow does not take starts nothing.
	if err := w.CheckTrigger(trigger); err != nil {
		var input *workflow.InputError
		if !errors.As(err, &input) {
			s.opts.Log("%s could not check a request: %v", w.Name, err)
			reply(rw, http.StatusInternalServerError, map[string]string{"error": "the workflow could not check the request body"})
			return
		}
		reply(rw, http.StatusBadRequest, map[string]any{
			"error":    "the request body does not match what this workflow takes",
			"problems": input.Problems,
		})
		return
	}
	source := "webhook " + r.URL.Path
	id := newID()

	if s.slots != nil {
		select {
		case s.slots <- struct{}{}:
		default:
			rw.Header().Set("Retry-After", "5")
			reply(rw, http.StatusTooManyRequests, map[string]string{"error": "too many runs are going; try again shortly"})
			return
		}
	}
	free := func() {
		if s.slots != nil {
			<-s.slots
		}
	}

	if w.Respond != nil {
		defer free()
		answer(rw, s.run(id, w, trigger, source))
		return
	}

	if r.URL.Query().Get("wait") == "true" {
		defer free()
		run := s.run(id, w, trigger, source)
		status := http.StatusOK
		if run.Status != "ok" {
			status = http.StatusInternalServerError
		}
		if s.opts.Token != "" {
			// Whoever sends webhooks is told how the run went, not what its
			// steps put out, which the page shows to those with its key.
			reply(rw, status, map[string]string{"run": run.ID, "status": run.Status})
			return
		}
		reply(rw, status, run)
		return
	}
	s.running.Add(1)
	go func() {
		defer s.running.Done()
		defer free()
		s.run(id, w, trigger, source)
	}()
	reply(rw, http.StatusAccepted, map[string]string{"run": id})
}

// bearer returns the token of a request's "Authorization: Bearer" header.
func bearer(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	return strings.TrimSpace(token), true
}

// refused notes a request turned away for a missing or wrong token or key.
// Under a flood of them the log gets a line every ten seconds.
func (s *Server) refused(r *http.Request) {
	l := &s.refusals
	l.Lock()
	defer l.Unlock()
	l.count++
	if time.Since(l.logged) < 10*time.Second {
		return
	}
	s.opts.Log("refused %d request(s) without the right token or key, the last: %s %s from %s", l.count, r.Method, r.URL.Path, r.RemoteAddr)
	l.count, l.logged = 0, time.Now()
}

// answer replies to the caller of a workflow that has respond.
func answer(rw http.ResponseWriter, run Run) {
	h := rw.Header()
	h.Set("Run-Id", run.ID)
	resp := run.Response
	if resp == nil {
		// Why it failed is in the run's record. It is not for the caller,
		// who may be anyone and need not see what the steps handled.
		reply(rw, http.StatusInternalServerError, map[string]string{"error": "the workflow failed", "run": run.ID})
		return
	}
	for name, value := range resp.Headers {
		h.Set(name, value)
	}
	switch body := resp.Body.(type) {
	case nil:
		rw.WriteHeader(resp.Status)
	case string:
		if h.Get("Content-Type") == "" {
			h.Set("Content-Type", "text/plain; charset=utf-8")
		}
		rw.WriteHeader(resp.Status)
		io.WriteString(rw, body)
	default:
		if h.Get("Content-Type") == "" {
			h.Set("Content-Type", "application/json")
		}
		rw.WriteHeader(resp.Status)
		json.NewEncoder(rw).Encode(body)
	}
}

// run executes the workflow to completion. It is not tied to the request that
// started it, so a caller that disconnects does not cancel the run.
func (s *Server) run(id string, w *workflow.Workflow, trigger any, source string) Run {
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()

	run := Run{
		ID: id, Workflow: w.Name, Source: source, Commit: s.commit(),
		Started: time.Now(), Status: running, Steps: []Step{}, Trigger: trigger,
	}
	s.history.put(run)
	s.opts.Log("%s %s started by %s", id, w.Name, source)

	eng := &engine.Engine{
		Model: s.opts.Model,
		HTTP:  s.opts.HTTP,
		Env:   s.opts.Env,
		OnStep: func(r engine.StepResult) {
			step := record(r)
			line := fmt.Sprintf("%s   %s %s", id, r.ID, r.Status)
			if r.Detail != "" {
				line += "  " + r.Detail
			}
			if r.Err != nil {
				line += "  " + firstLine(step.Error)
			}
			run.Steps = append(run.Steps, step)
			run.Tokens.Add(r.Tokens)
			s.history.put(run)
			// Step outputs can hold customer data, so they are not logged.
			s.opts.Log("%s", line)
		},
	}
	resp, err := eng.Run(ctx, w, trigger)
	run.Status, run.Response = "ok", resp
	if err != nil {
		run.Status, run.Error = "failed", err.Error()
	}
	run.Millis = time.Since(run.Started).Milliseconds()
	s.history.put(run)
	s.opts.Log("%s %s %s in %s%s", id, w.Name, run.Status, time.Duration(run.Millis)*time.Millisecond, tokensOf(run.Tokens))

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

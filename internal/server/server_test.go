package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"aia/internal/ai"
	"aia/internal/workflow"
)

// fakeModel decides that every ticket has the given priority.
type fakeModel struct{ priority string }

func (m fakeModel) Generate(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{Text: `{"summary": "Something is wrong."}`, Model: "fake-writer"}, nil
}

func (m fakeModel) Decide(context.Context, ai.DecideRequest) (ai.DecideResponse, error) {
	return ai.DecideResponse{Model: "fake-decider", Answers: map[string]any{
		"category": map[string]any{"value": "outage"},
		"priority": map[string]any{"value": m.priority},
	}}, nil
}

// calls records the requests that workflows' http steps make.
type calls struct {
	mu   sync.Mutex
	seen []string
}

func (c *calls) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.seen = append(c.seen, r.URL.Path+" "+string(body))
	c.mu.Unlock()
}

type fixture struct {
	srv   *Server
	calls *calls
	logs  *[]string
	runs  *[]Run
}

// serveExample serves the triage-ticket example with a fake model, its http
// steps pointed at a recording server.
func serveExample(t *testing.T, priority, token string) fixture {
	t.Helper()
	w, err := workflow.Load("../../examples/triage-ticket")
	if err != nil {
		t.Fatal(err)
	}
	c := &calls{}
	sink := httptest.NewServer(c)
	t.Cleanup(sink.Close)

	var mu sync.Mutex
	var logs []string
	var runs []Run
	srv, err := New([]*workflow.Workflow{w}, Options{
		Model: fakeModel{priority},
		HTTP:  sink.Client(),
		Env:   map[string]string{"PAGER_URL": sink.URL + "/pager", "BILLING_URL": sink.URL + "/billing"},
		Token: token,
		Log: func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			logs = append(logs, fmt.Sprintf(format, args...))
		},
		OnRun: func(r Run) {
			mu.Lock()
			defer mu.Unlock()
			runs = append(runs, r)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return fixture{srv, c, &logs, &runs}
}

// request builds a request addressed to the server the way a local client
// would address it.
func request(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = "127.0.0.1:8080"
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func do(srv *Server, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func post(srv *Server, target, token, body string) *httptest.ResponseRecorder {
	req := request(http.MethodPost, target, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return do(srv, req)
}

func get(srv *Server, target string) *httptest.ResponseRecorder {
	return do(srv, request(http.MethodGet, target, ""))
}

const ticket = `{"subject": "Dashboard returns 502", "customer": "ops@example.org"}`

func TestWebhookRunsInBackground(t *testing.T) {
	f := serveExample(t, "high", "")
	rec := post(f.srv, "/tickets", "", ticket)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var accepted struct{ Run string }
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil || !strings.HasPrefix(accepted.Run, "r-") {
		t.Fatalf("body = %s", rec.Body)
	}

	f.srv.Wait()
	want := `/pager {"category":"outage","summary":"Something is wrong."}`
	if len(f.calls.seen) != 1 || f.calls.seen[0] != want {
		t.Errorf("http steps sent %q, want one %q", f.calls.seen, want)
	}
	if len(*f.runs) != 1 || (*f.runs)[0].ID != accepted.Run || (*f.runs)[0].Status != "ok" {
		t.Errorf("runs = %+v, want one ok run with the id that was returned", *f.runs)
	}

	logged := strings.Join(*f.logs, "\n")
	for _, want := range []string{
		accepted.Run + " triage-ticket started by webhook /tickets",
		accepted.Run + "   classify ok  fake-decider, 0 tokens in",
		accepted.Run + "   alert_billing skipped",
		accepted.Run + " triage-ticket ok in ",
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("log has no line %q:\n%s", want, logged)
		}
	}
	if strings.Contains(logged, "ops@example.org") || strings.Contains(logged, "Something is wrong") {
		t.Errorf("log contains ticket or output data:\n%s", logged)
	}
}

func TestWebhookWait(t *testing.T) {
	f := serveExample(t, "low", "")
	rec := post(f.srv, "/tickets?wait=true", "", ticket)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var run Run
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range run.Steps {
		got = append(got, s.ID+"="+s.Status)
	}
	want := "classify=ok summarize=skipped alert_billing=skipped page_oncall=skipped"
	if run.Workflow != "triage-ticket" || run.Status != "ok" || strings.Join(got, " ") != want {
		t.Errorf("run = %+v, want an ok run with steps %s", run, want)
	}
	priority := run.Steps[0].Output.(map[string]any)["priority"].(map[string]any)["value"]
	if priority != "low" {
		t.Errorf("classify output = %v, want the decision", run.Steps[0].Output)
	}
	if len(f.calls.seen) != 0 {
		t.Errorf("http steps sent %q for a low-priority ticket", f.calls.seen)
	}
}

func TestWebhookWaitReportsFailure(t *testing.T) {
	f := serveExample(t, "high", "")
	f.srv.opts.HTTP = &http.Client{Transport: failing{}}
	rec := post(f.srv, "/tickets?wait=true", "", ticket)

	var run Run
	json.Unmarshal(rec.Body.Bytes(), &run)
	if rec.Code != http.StatusInternalServerError || run.Status != "failed" || !strings.HasPrefix(run.Error, `step "page_oncall" failed`) {
		t.Errorf("status = %d, run = %+v", rec.Code, run)
	}
	last := run.Steps[len(run.Steps)-1]
	if last.ID != "page_oncall" || last.Status != "failed" || last.Error == "" {
		t.Errorf("last step = %+v", last)
	}
}

type failing struct{}

func (failing) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("connection refused")
}

func TestRequestsThatDoNotStartARun(t *testing.T) {
	f := serveExample(t, "high", "secret-token")
	const asJSON, form = "application/json; charset=utf-8", "application/x-www-form-urlencoded"
	tests := []struct {
		name          string
		method, path  string
		token         string
		contentType   string
		body          string
		status        int
		errorContains string
	}{
		{"unknown path", "POST", "/other", "secret-token", asJSON, ticket, http.StatusNotFound, "no workflow"},
		{"GET", "GET", "/tickets", "secret-token", "", "", http.StatusMethodNotAllowed, "POST"},
		{"no token", "POST", "/tickets", "", asJSON, ticket, http.StatusUnauthorized, "bearer token"},
		{"wrong token", "POST", "/tickets", "secret-tokem", asJSON, ticket, http.StatusUnauthorized, "bearer token"},
		{"sent as a form", "POST", "/tickets", "secret-token", form, ticket, http.StatusUnsupportedMediaType, "application/json"},
		{"no content type", "POST", "/tickets", "secret-token", "", ticket, http.StatusUnsupportedMediaType, "application/json"},
		{"body is not JSON", "POST", "/tickets", "secret-token", asJSON, "subject=hi", http.StatusBadRequest, "JSON"},
		{"empty body", "POST", "/tickets", "secret-token", asJSON, "", http.StatusBadRequest, "JSON"},
		{"body too large", "POST", "/tickets", "secret-token", asJSON, `"` + strings.Repeat("x", maxBody) + `"`, http.StatusRequestEntityTooLarge, "1 MB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			rec := do(f.srv, req)
			if rec.Code != tt.status || !strings.Contains(rec.Body.String(), tt.errorContains) {
				t.Errorf("status = %d, body = %s; want %d mentioning %q", rec.Code, rec.Body, tt.status, tt.errorContains)
			}
		})
	}
	f.srv.Wait()
	if len(*f.runs) != 0 || len(f.calls.seen) != 0 {
		t.Errorf("rejected requests started %d runs and sent %q", len(*f.runs), f.calls.seen)
	}

	if rec := post(f.srv, "/tickets?wait=true", "secret-token", ticket); rec.Code != http.StatusOK {
		t.Errorf("request with the right token: status = %d, body %s", rec.Code, rec.Body)
	}
	if rec := do(f.srv, httptest.NewRequest("GET", "/healthz", nil)); rec.Code != http.StatusOK {
		t.Errorf("/healthz without a token: status = %d", rec.Code)
	}
}

// writeWorkflow creates a directory holding one workflow file.
func writeWorkflow(t *testing.T, content string) *workflow.Workflow {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "workflow.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := workflow.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

const hook = `name: %s
trigger:
  %s
steps:
  - id: notify
    http:
      method: POST
      url: ${env.%s}
`

func TestNewRefusesWorkflowsThatCannotBeServed(t *testing.T) {
	a := writeWorkflow(t, fmt.Sprintf(hook, "a", "webhook: /hook", "A_URL"))
	b := writeWorkflow(t, fmt.Sprintf(hook, "b", "webhook: /hook", "B_URL"))
	nightly := writeWorkflow(t, fmt.Sprintf(hook, "nightly", `cron: "0 3 * * *"`, "NIGHTLY_URL"))
	manual := writeWorkflow(t, fmt.Sprintf(hook, "manual", "manual: true", "MANUAL_URL"))
	env := map[string]string{"A_URL": "http://a", "B_URL": "http://b", "NIGHTLY_URL": "http://n"}

	tests := []struct {
		name      string
		workflows []*workflow.Workflow
		env       map[string]string
		want      string
	}{
		{"shared webhook path", []*workflow.Workflow{a, b}, env, "both use the webhook path /hook"},
		{"unset variables", []*workflow.Workflow{nightly, a}, map[string]string{"B_URL": "x"}, "not set: A_URL, NIGHTLY_URL"},
		{"nothing with a trigger", []*workflow.Workflow{manual}, env, "nothing to serve"},
		{
			"webhook path the server uses",
			[]*workflow.Workflow{writeWorkflow(t, fmt.Sprintf(hook, "c", "webhook: /api/runs", "A_URL"))}, env,
			"the webhook path /api/runs is used by the server itself",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.workflows, Options{Env: tt.env})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want one containing %q", err, tt.want)
			}
		})
	}

	// A workflow without a trigger is left out; its variables do not matter.
	srv, err := New([]*workflow.Workflow{a, nightly, manual}, Options{Env: env})
	if err != nil {
		t.Fatal(err)
	}
	routes := strings.Join(srv.Routes(), "\n")
	if !strings.Contains(routes, "POST /hook  ->  a") || !strings.Contains(routes, "cron 0 3 * * *  ->  nightly") || strings.Contains(routes, "manual") {
		t.Errorf("routes:\n%s", routes)
	}
	srv.Start()()
}

func TestOnlyLocalNamesAreAnsweredWithoutAToken(t *testing.T) {
	f := serveExample(t, "low", "")
	for host, want := range map[string]int{
		"127.0.0.1:8080":   http.StatusOK,
		"localhost:8080":   http.StatusOK,
		"[::1]:8080":       http.StatusOK,
		"localhost":        http.StatusOK,
		"evil.example":     http.StatusForbidden,
		"evil.example:80":  http.StatusForbidden,
		"192.168.1.20:808": http.StatusForbidden,
	} {
		page := request(http.MethodGet, "/api/state", "")
		page.Host = host
		hook := request(http.MethodPost, "/tickets?wait=true", ticket)
		hook.Host = host
		if got := do(f.srv, page).Code; got != want {
			t.Errorf("GET /api/state with Host %s: status = %d, want %d", host, got, want)
		}
		if got := do(f.srv, hook).Code; got != want {
			t.Errorf("POST /tickets with Host %s: status = %d, want %d", host, got, want)
		}
	}

	// With a token, the token is the protection and any host name is served.
	withToken := serveExample(t, "low", "secret-token")
	req := request(http.MethodGet, "/api/state", "")
	req.Host = "aia.example.com"
	req.SetBasicAuth("anyone", "secret-token")
	if rec := do(withToken.srv, req); rec.Code != http.StatusOK {
		t.Errorf("with a token and a public host name: status = %d", rec.Code)
	}
}

func TestPageNeedsTheToken(t *testing.T) {
	f := serveExample(t, "low", "secret-token")
	for _, path := range []string{"/", "/ui/app.js", "/api/state", "/api/runs/r-0"} {
		rec := get(f.srv, path)
		if rec.Code != http.StatusUnauthorized || !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Basic ") {
			t.Errorf("GET %s without the token: status = %d, WWW-Authenticate = %q", path, rec.Code, rec.Header().Get("WWW-Authenticate"))
		}
	}

	wrong := request(http.MethodGet, "/", "")
	wrong.SetBasicAuth("admin", "guess")
	basic := request(http.MethodGet, "/", "")
	basic.SetBasicAuth("anyone", "secret-token")
	bearer := request(http.MethodGet, "/api/state", "")
	bearer.Header.Set("Authorization", "Bearer secret-token")
	if got := []int{do(f.srv, wrong).Code, do(f.srv, basic).Code, do(f.srv, bearer).Code}; got[0] != 401 || got[1] != 200 || got[2] != 200 {
		t.Errorf("wrong password, right password, bearer token: statuses = %v, want [401 200 200]", got)
	}

	// Basic authentication is for the page only: a browser sends it along with
	// requests another site makes, so it must not start a run.
	hook := request(http.MethodPost, "/tickets?wait=true", ticket)
	hook.SetBasicAuth("anyone", "secret-token")
	if rec := do(f.srv, hook); rec.Code != http.StatusUnauthorized {
		t.Errorf("webhook with basic authentication: status = %d, want 401", rec.Code)
	}
}

func TestPageFiles(t *testing.T) {
	f := serveExample(t, "low", "")
	for path, want := range map[string][2]string{
		"/":           {"text/html; charset=utf-8", `<script src="/ui/app.js"></script>`},
		"/ui/app.js":  {"text/javascript; charset=utf-8", `getJSON("/api/state")`},
		"/ui/app.css": {"text/css; charset=utf-8", ".card {"},
	} {
		rec := get(f.srv, path)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != want[0] || !strings.Contains(rec.Body.String(), want[1]) {
			t.Errorf("GET %s: status = %d, type = %q, body has %q: %v", path, rec.Code, rec.Header().Get("Content-Type"), want[1], strings.Contains(rec.Body.String(), want[1]))
		}
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "script-src 'self'") {
			t.Errorf("GET %s: Content-Security-Policy = %q", path, csp)
		}
	}
	if rec := get(f.srv, "/ui/missing.js"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /ui/missing.js: status = %d", rec.Code)
	}
	// The page must not need inline code or styles, which its policy blocks.
	index, _ := ui.ReadFile("ui/index.html")
	if strings.Contains(string(index), "<style") || strings.Contains(string(index), "onclick=") || strings.Count(string(index), "<script") != 1 {
		t.Error("index.html has inline code or styles")
	}
}

func TestStateDescribesWorkflowsAndRuns(t *testing.T) {
	f := serveExample(t, "high", "")
	f.srv.opts.Commit = "abc1234"
	var finished Run
	json.Unmarshal(post(f.srv, "/tickets?wait=true", "", ticket).Body.Bytes(), &finished)

	var state stateView
	rec := get(f.srv, "/api/state")
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Commit != "abc1234" || state.Protected || len(state.Workflows) != 1 || len(state.Runs) != 1 {
		t.Fatalf("state = %+v", state)
	}

	w := state.Workflows[0]
	if w.Name != "triage-ticket" || w.Trigger != "POST /tickets" || w.Tests != 3 || len(w.Steps) != 4 {
		t.Fatalf("workflow = %+v", w)
	}
	want := []stepView{
		{ID: "classify", Kind: "decide", Subtitle: "clef", Reads: []string{"trigger"},
			Fields: [][2]string{{"model", "openrouter/cloudflare/clef"}},
			Questions: []questionView{
				{Name: "category", Ask: "What does the customer need?", Answers: "one of refund, billing, outage, bug, account, other", Options: []ai.Option{
					{Name: "refund", Meaning: "They want money back for a charge."},
					{Name: "billing", Meaning: "An invoice, plan, or payment question that is not a refund."},
					{Name: "outage", Meaning: "The product is down or unusable for them."},
					{Name: "bug", Meaning: "Something behaves incorrectly but the product is still usable."},
					{Name: "account", Meaning: "Login, access, or profile changes."},
					{Name: "other", Meaning: "Anything else."},
				}},
				{Name: "priority", Ask: "How urgent is this ticket? Judge by impact, not by tone.", Answers: "score: low < medium < high", Options: []ai.Option{
					{Name: "low", Meaning: "Questions, feedback, and requests with no time pressure."},
					{Name: "medium", Meaning: "The customer is blocked on one task but has a workaround or can wait a day."},
					{Name: "high", Meaning: "The customer cannot use the product, or data or money is at risk right now."},
				}},
			}},
		{ID: "summarize", Kind: "ai", Subtitle: "deepseek-v4.1-flash", When: `classify.priority.value == "high"`, Reads: []string{"trigger", "classify"}, Fields: [][2]string{
			{"model", "openrouter/deepseek/deepseek-v4.1-flash"},
			{"prompt", "prompts/summarize.md"},
			{"output", "schemas/summary.json"},
		}},
		{ID: "alert_billing", Kind: "http", Subtitle: "POST ${env.BILLING_URL}", When: `classify.priority.value == "high" && classify.category.value in ["refund", "billing"]`,
			Reads: []string{"classify", "summarize"}, Fields: [][2]string{
				{"request", "POST ${env.BILLING_URL}"},
				{"sends", "category, summary"},
			}},
	}
	for i, step := range want {
		if got, _ := json.Marshal(w.Steps[i]); string(got) != mustJSON(step) {
			t.Errorf("step %d:\n got %s\nwant %s", i+1, got, mustJSON(step))
		}
	}

	// The list of runs leaves out what the detail of one run has.
	listed := state.Runs[0]
	if listed.ID != finished.ID || listed.Status != "ok" || listed.Commit != "abc1234" || listed.Source != "webhook /tickets" || listed.Trigger != nil {
		t.Errorf("listed run = %+v", listed)
	}
	for _, step := range listed.Steps {
		if step.Output != nil {
			t.Errorf("listed run includes the output of %s", step.ID)
		}
	}

	var full Run
	json.Unmarshal(get(f.srv, "/api/runs/"+finished.ID).Body.Bytes(), &full)
	if full.Trigger == nil || full.Steps[0].Output == nil || full.Steps[0].Detail != "fake-decider, 0 tokens in" || full.Started.IsZero() {
		t.Errorf("run detail = %+v", full)
	}
	if rec := get(f.srv, "/api/runs/r-missing"); rec.Code != http.StatusNotFound {
		t.Errorf("GET an unknown run: status = %d", rec.Code)
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestHistorySurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "runs.jsonl")
	w, err := workflow.Load("../../examples/triage-ticket")
	if err != nil {
		t.Fatal(err)
	}
	start := func() *Server {
		srv, err := New([]*workflow.Workflow{w}, Options{
			Model:   fakeModel{"low"},
			Env:     map[string]string{"PAGER_URL": "http://unused", "BILLING_URL": "http://unused"},
			History: path,
		})
		if err != nil {
			t.Fatal(err)
		}
		return srv
	}

	first := start()
	var ran Run
	json.Unmarshal(post(first, "/tickets?wait=true", "", ticket).Body.Bytes(), &ran)
	first.Close()

	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("history file: %v, mode %v; want it readable by its owner only", err, info.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	if strings.Count(string(data), "\n") != 1 {
		t.Errorf("history file has %d lines after one run, want only the finished run", strings.Count(string(data), "\n"))
	}
	// A record cut short by a crash must not stop the server from starting.
	os.WriteFile(path, append(data, []byte(`{"run": "r-cut", "workflow": "tri`)...), 0o600)

	second := start()
	defer second.Close()
	runs := second.history.recent()
	if len(runs) != 1 || runs[0].ID != ran.ID || runs[0].Status != "ok" || runs[0].Trigger == nil {
		t.Errorf("runs after a restart = %+v, want the one finished run with its trigger", runs)
	}
}

func TestHistoryKeepsTheMostRecentRuns(t *testing.T) {
	h, err := openHistory("")
	if err != nil {
		t.Fatal(err)
	}
	for i := range historySize + 3 {
		h.put(Run{ID: fmt.Sprintf("r-%d", i), Status: "ok"})
	}
	h.put(Run{ID: fmt.Sprintf("r-%d", historySize+2), Status: "failed"})

	runs := h.recent()
	if len(runs) != historySize || runs[0].ID != fmt.Sprintf("r-%d", historySize+2) || runs[0].Status != "failed" || runs[len(runs)-1].ID != "r-3" {
		t.Errorf("kept %d runs, newest %+v, oldest %s", len(runs), runs[0], runs[len(runs)-1].ID)
	}
	if _, ok := h.get("r-0"); ok {
		t.Error("the oldest run was not dropped")
	}
}

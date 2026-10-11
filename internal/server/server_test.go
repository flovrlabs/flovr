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

func post(srv *Server, target, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
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
	tests := []struct {
		name          string
		method, path  string
		token, body   string
		status        int
		errorContains string
	}{
		{"unknown path", "POST", "/other", "secret-token", ticket, http.StatusNotFound, "no workflow"},
		{"GET", "GET", "/tickets", "secret-token", "", http.StatusMethodNotAllowed, "POST"},
		{"no token", "POST", "/tickets", "", ticket, http.StatusUnauthorized, "bearer token"},
		{"wrong token", "POST", "/tickets", "secret-tokem", ticket, http.StatusUnauthorized, "bearer token"},
		{"body is not JSON", "POST", "/tickets", "secret-token", "subject=hi", http.StatusBadRequest, "JSON"},
		{"empty body", "POST", "/tickets", "secret-token", "", http.StatusBadRequest, "JSON"},
		{"body too large", "POST", "/tickets", "secret-token", `"` + strings.Repeat("x", maxBody) + `"`, http.StatusRequestEntityTooLarge, "1 MB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			rec := httptest.NewRecorder()
			f.srv.ServeHTTP(rec, req)
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
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
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

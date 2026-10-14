package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/simonyosb/flowmaker/internal/ai"
	"github.com/simonyosb/flowmaker/internal/workflow"
)

// moderator answers the questions of the moderate-comment example, or fails.
type moderator struct {
	allowed bool
	reason  string
	err     error
}

func (m moderator) Generate(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{}, errors.New("this workflow has no ai step")
}

func (m moderator) Decide(context.Context, ai.DecideRequest) (ai.DecideResponse, error) {
	return ai.DecideResponse{Model: "fake-decider", Answers: map[string]any{
		"allowed": map[string]any{"value": m.allowed},
		"reason":  map[string]any{"value": m.reason},
	}}, m.err
}

func serveEndpoint(t *testing.T, model moderator) *Server {
	t.Helper()
	w, err := workflow.Load("../../examples/moderate-comment")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New([]*workflow.Workflow{w}, Options{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestEndpointAnswersItsCaller(t *testing.T) {
	srv := serveEndpoint(t, moderator{allowed: false, reason: "spam"})
	rec := post(srv, "/moderate", "", `{"text": "CHEAP WATCHES", "author": "deals"}`)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status = %d, content type %q, body %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"allowed":false,"reason":"spam"}` {
		t.Errorf("body = %s", got)
	}

	// The run is recorded with what was answered; the list leaves that out.
	id := rec.Header().Get("Run-Id")
	var full Run
	if err := json.Unmarshal(get(srv, "/api/runs/"+id).Body.Bytes(), &full); err != nil {
		t.Fatal(err)
	}
	if full.Status != "ok" || full.Response == nil || full.Response.Status != http.StatusOK || mustJSON(full.Response.Body) != `{"allowed":false,"reason":"spam"}` {
		t.Errorf("run = %+v, response %+v", full, full.Response)
	}
	state := currentState(t, srv)
	if len(state.Runs) != 1 || state.Runs[0].ID != id || state.Runs[0].Response != nil {
		t.Errorf("listed runs = %+v", state.Runs)
	}

	// The page is told what the endpoint takes and answers.
	w := state.Workflows[0]
	if w.Input != "schemas/comment.json" || w.Respond == nil || w.Respond.Status != http.StatusOK {
		t.Fatalf("workflow = %+v", w)
	}
	if got := mustJSON(w.Respond); got != `{"status":200,"body":{"allowed":"${check.allowed.value}","reason":"${check.reason.value}"},"reads":["check"]}` {
		t.Errorf("respond = %s", got)
	}
}

func TestEndpointRefusesABodyItDoesNotTake(t *testing.T) {
	srv := serveEndpoint(t, moderator{allowed: true, reason: "none"})
	for body, want := range map[string]string{
		`{"author": "dana"}`: "missing property 'text'",
		`{"text": ""}`:       "/text: ",
		`{"text": 5}`:        "/text: got number, want string",
		`"just text"`:        "the body: got string, want object",
	} {
		rec := post(srv, "/moderate", "", body)
		var answer struct {
			Error    string
			Problems []string
		}
		json.Unmarshal(rec.Body.Bytes(), &answer)
		if rec.Code != http.StatusBadRequest || len(answer.Problems) != 1 || !strings.Contains(answer.Problems[0], want) {
			t.Errorf("body %s: status = %d, answer %s; want a problem containing %q", body, rec.Code, rec.Body, want)
		}
	}
	if runs := currentState(t, srv).Runs; len(runs) != 0 {
		t.Errorf("%d runs were started by requests that were refused", len(runs))
	}
}

func TestEndpointDoesNotTellItsCallerWhyItFailed(t *testing.T) {
	srv := serveEndpoint(t, moderator{err: errors.New("the model said: internal detail about customer 4711")})
	rec := post(srv, "/moderate", "", `{"text": "hello"}`)
	id := rec.Header().Get("Run-Id")
	if rec.Code != http.StatusInternalServerError || strings.TrimSpace(rec.Body.String()) != `{"error":"the workflow failed","run":"`+id+`"}` {
		t.Errorf("status = %d, body %s", rec.Code, rec.Body)
	}
	// The reason is in the run's record, for whoever may see the page.
	var full Run
	json.Unmarshal(get(srv, "/api/runs/"+id).Body.Bytes(), &full)
	if full.Status != "failed" || !strings.Contains(full.Error, "customer 4711") || full.Response != nil {
		t.Errorf("run = %+v", full)
	}
}

func TestEndpointCanAnswerWithTextAndItsOwnStatus(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "workflow.yaml"), []byte(`name: ping
trigger:
  webhook: /ping
steps:
  - id: echo
    when: trigger.body.skip != true
    http: {method: GET, url: "http://unused.invalid/"}
respond:
  status: 202
  headers:
    X-Name: ${trigger.body.name}
  body: Hello, ${trigger.body.name}.
`), 0o644)
	w, err := workflow.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New([]*workflow.Workflow{w}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	rec := post(srv, "/ping", "", `{"name": "dana", "skip": true}`)
	if rec.Code != http.StatusAccepted || rec.Body.String() != "Hello, dana." ||
		rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" || rec.Header().Get("X-Name") != "dana" {
		t.Errorf("status = %d, headers %v, body %q", rec.Code, rec.Header(), rec.Body)
	}
}

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/simonyosb/flowmaker/internal/ai"
	"github.com/simonyosb/flowmaker/internal/workflow"
)

// replier finds no ticket urgent and drafts the same reply to each.
type replier struct{}

func (replier) Generate(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{Text: `{"subject": "Re: Dashboard", "text": "We are looking into it."}`, Model: "fake-writer"}, nil
}

func (replier) Decide(context.Context, ai.DecideRequest) (ai.DecideResponse, error) {
	return ai.DecideResponse{Model: "fake-decider", Answers: map[string]any{
		"category": map[string]any{"value": "outage"},
		"priority": map[string]any{"value": "low"},
	}}, nil
}

func TestStepThatUsesAComponentMadeOfSteps(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "triage-ticket")
	if err := os.CopyFS(dir, os.DirFS("../../examples/triage-ticket")); err != nil {
		t.Fatal(err)
	}
	w, err := workflow.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := &calls{}
	sink := httptest.NewServer(c)
	t.Cleanup(sink.Close)
	srv, err := New([]*workflow.Workflow{w}, Options{
		Model: replier{}, HTTP: sink.Client(), Edit: true,
		Env: map[string]string{"PAGER_URL": sink.URL + "/pager", "BILLING_URL": sink.URL + "/billing", "MAIL_URL": sink.URL + "/mail"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The page adds it like any other component.
	rec := steps(t, srv, `"add": {"id": "answer", "use": "components/reply", "with": {"ticket": "${trigger.body}", "url": "${env.MAIL_URL}"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	step := currentState(t, srv).Workflows[0].Steps[4]
	want := stepView{
		ID: "answer", Kind: "flow", Subtitle: "reply", Reads: []string{"trigger"},
		Fields:    [][2]string{{"component", "components/reply"}, {"gives back", "subject"}},
		Component: "reply", Uses: "components/reply", Target: filepath.ToSlash(filepath.Join(dir, "components", "reply")),
		Inner: []innerView{
			{ID: "draft", Kind: "ai", Subtitle: "deepseek-v4.1-flash"},
			{ID: "send", Kind: "http", Subtitle: "POST ${input.url}"},
		},
		Outputs: []string{"subject"},
	}
	step.Edit = nil
	if got := mustJSON(step); got != mustJSON(want) {
		t.Errorf("step:\n got %s\nwant %s", got, mustJSON(want))
	}

	// A run records what each step inside did.
	var run Run
	rec = post(srv, "/tickets?wait=true", "", ticket)
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil || run.Status != "ok" || len(run.Steps) != 5 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	answer := run.Steps[4]
	if answer.ID != "answer" || answer.Kind != "flow" || answer.Status != "ok" || answer.Detail != "ran draft, send" || mustJSON(answer.Output) != `{"subject":"Re: Dashboard"}` {
		t.Errorf("answer = %+v", answer)
	}
	if len(answer.Steps) != 2 || answer.Steps[0].ID != "draft" || answer.Steps[0].Kind != "ai" || answer.Steps[0].Output == nil ||
		answer.Steps[1].ID != "send" || answer.Steps[1].Status != "ok" || !strings.HasSuffix(answer.Steps[1].Detail, "-> 200") {
		t.Errorf("steps inside = %+v", answer.Steps)
	}
	if len(c.seen) != 1 || c.seen[0] != `/mail {"subject":"Re: Dashboard","text":"We are looking into it."}` {
		t.Errorf("requests = %q", c.seen)
	}

	// The list of runs keeps the steps inside, without what they put out.
	listed := currentState(t, srv).Runs[0].Steps[4]
	if listed.Output != nil || len(listed.Steps) != 2 || listed.Steps[0].Output != nil || listed.Steps[0].Status != "ok" {
		t.Errorf("listed = %+v", listed)
	}
}

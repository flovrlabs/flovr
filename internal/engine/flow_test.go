package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/flovrlabs/flovr/internal/workflow"
)

// flowProject is a workflow whose step uses a component made of steps, one
// of which uses another component.
var flowProject = map[string]string{
	"workflow.yaml": `name: support
trigger:
  webhook: /tickets
steps:
  - id: answer
    use: components/reply
    with:
      ticket: ${trigger.body}
      url: ${env.MAIL_URL}
  - id: log
    http:
      method: POST
      url: ${env.MAIL_URL}/log
      body: {subject: "${answer.subject}", sent: "${answer.sent}"}
`,
	"components/reply/component.yaml": `name: reply
input:
  ticket: object
  url: string
steps:
  - id: draft
    ai: {model: anthropic/m, prompt: prompt.md, input: "${input.ticket}", output: schema.json}
  - id: send
    when: '!("urgent" in input.ticket)'
    use: ../notify
    with:
      url: ${input.url}
      text: ${draft.summary}
  - id: page
    when: '"urgent" in input.ticket'
    use: ../notify
    with:
      url: ${input.url}/page
      text: ${draft.summary}
output:
  subject: "Re: ${draft.summary}"
  sent: ${send != null}
tests:
  - name: sends what it drafts
    input: {ticket: {subject: Down}, url: "http://unreachable.invalid"}
    expect:
      send: called
      page: skipped
      draft.summary: Dashboard is down.
      output.subject: "Re: Dashboard is down."
      output.sent: true
  - name: expects the other step
    input: {ticket: {subject: Down}, url: "http://unreachable.invalid"}
    expect:
      send: skipped
      page: called
`,
	"components/reply/prompt.md":   "Draft a reply.",
	"components/reply/schema.json": `{"type": "object", "required": ["summary"], "properties": {"summary": {"type": "string"}}}`,
	"components/notify/component.yaml": `name: notify
input:
  url: string
  text: string
http:
  method: POST
  url: ${input.url}
  body:
    text: ${input.text}
`,
}

func TestStepRunsAComponentMadeOfSteps(t *testing.T) {
	w, err := workflow.Load(writeProject(t, flowProject))
	if err != nil {
		t.Fatal(err)
	}
	p := &pager{status: http.StatusOK}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)

	model := &fakeModel{answer: `{"summary": "Dashboard is down."}`}
	var results []StepResult
	eng := &Engine{
		Model:  model,
		HTTP:   srv.Client(),
		Env:    map[string]string{"MAIL_URL": srv.URL + "/mail"},
		OnStep: func(r StepResult) { results = append(results, r) },
	}
	trigger := map[string]any{"body": map[string]any{"subject": "Dashboard returns 502"}}
	if _, err := eng.Run(context.Background(), w, trigger); err != nil {
		t.Fatal(err)
	}

	// The workflow sees one step, which holds what the steps inside did.
	if got, want := statuses(results), []string{"answer=ok", "log=ok"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("statuses = %v, want %v", got, want)
	}
	answer := results[0]
	if answer.Kind != "flow" || answer.Detail != "ran draft, send; skipped page" {
		t.Errorf("answer: kind %q, detail %q", answer.Kind, answer.Detail)
	}
	if got, want := statuses(answer.Steps), []string{"draft=ok", "send=ok", "page=skipped"}; !reflect.DeepEqual(got, want) {
		t.Errorf("steps inside = %v, want %v", got, want)
	}
	if got := []string{answer.Steps[0].Kind, answer.Steps[1].Kind}; !reflect.DeepEqual(got, []string{"ai", "http"}) {
		t.Errorf("kinds inside = %v", got)
	}
	// It gives back what its output says, and nothing else of its steps.
	if want := map[string]any{"subject": "Re: Dashboard is down.", "sent": true}; !reflect.DeepEqual(answer.Output, want) {
		t.Errorf("output = %v, want %v", answer.Output, want)
	}
	// The model was given the prompt in the component's folder.
	if model.got.System != "Draft a reply." {
		t.Errorf("prompt = %q", model.got.System)
	}
	want := []string{
		`POST /mail application/json {"text":"Dashboard is down."}`,
		`POST /mail/log application/json {"sent":true,"subject":"Re: Dashboard is down."}`,
	}
	if !reflect.DeepEqual(p.bodies, want) {
		t.Errorf("requests = %q, want %q", p.bodies, want)
	}
}

func TestStepFailsWhereAStepInsideFails(t *testing.T) {
	w, err := workflow.Load(writeProject(t, flowProject))
	if err != nil {
		t.Fatal(err)
	}
	p := &pager{status: http.StatusBadGateway}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)

	var results []StepResult
	eng := &Engine{
		Model:  &fakeModel{answer: `{"summary": "Dashboard is down."}`},
		HTTP:   srv.Client(),
		Env:    map[string]string{"MAIL_URL": srv.URL + "/mail"},
		OnStep: func(r StepResult) { results = append(results, r) },
	}
	_, err = eng.Run(context.Background(), w, map[string]any{"body": map[string]any{"subject": "x"}})
	if want := `step "answer" failed: step "send" of reply failed: the server answered 502 Bad Gateway`; err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
	// The steps after the one that failed, inside and outside, did not run.
	if len(results) != 1 || results[0].Status != Failed || results[0].Detail != "stopped at send" {
		t.Fatalf("results = %+v", results)
	}
	if got, want := statuses(results[0].Steps), []string{"draft=ok", "send=failed"}; !reflect.DeepEqual(got, want) {
		t.Errorf("steps inside = %v, want %v", got, want)
	}
	if len(p.bodies) != 1 {
		t.Errorf("%d requests were sent, want the one that failed", len(p.bodies))
	}
}

func TestComponentMadeOfStepsIsTestedOnItsOwn(t *testing.T) {
	dir := writeProject(t, flowProject)
	reply, err := workflow.LoadComponent(filepath.Join(dir, "components", "reply"))
	if err != nil {
		t.Fatal(err)
	}
	eng := &Engine{Model: &fakeModel{answer: `{"summary": "Dashboard is down."}`}}

	res, err := eng.TestComponent(context.Background(), reply, reply.Tests[0])
	if err != nil || len(res.Failures) != 0 {
		t.Errorf("first test: failures %q, %v", res.Failures, err)
	}
	if len(res.Steps) != 1 || len(res.Steps[0].Steps) != 3 || res.Steps[0].Steps[1].Detail != "POST, not sent" {
		t.Errorf("first test: steps %+v", res.Steps)
	}
	res, err = eng.TestComponent(context.Background(), reply, reply.Tests[1])
	want := []string{"page: expected called, but it was skipped", "send: expected skipped, but it was called"}
	if err != nil || !reflect.DeepEqual(res.Failures, want) {
		t.Errorf("second test: failures %q, %v; want %q", res.Failures, err, want)
	}

	// A step inside that fails fails the test, with where it happened.
	bad := &Engine{Model: &fakeModel{answer: `{"summary": 5}`}}
	res, err = bad.TestComponent(context.Background(), reply, reply.Tests[0])
	if err != nil || len(res.Failures) != 1 || !strings.HasPrefix(res.Failures[0], `step "draft" of reply failed: the model's answer does not match schema.json`) {
		t.Errorf("a step that fails: %q, %v", res.Failures, err)
	}
}

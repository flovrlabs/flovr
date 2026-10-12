package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"aia/internal/ai"
	"aia/internal/workflow"
)

// componentProject is a workflow whose three steps each use a component.
var componentProject = map[string]string{
	"workflow.yaml": `name: support
trigger:
  webhook: /tickets
steps:
  - id: route
    use: components/route
    with:
      text: ${trigger.body.subject}
  - id: summary
    use: components/summarize
    with:
      ticket: ${trigger.body}
      audience: engineers
  - id: tell
    when: route.urgent.value
    use: components/notify
    with:
      url: ${env.TEAM_URL}
      text: "${summary.summary} (${trigger.body.customer})"
`,
	"components/route/component.yaml": `name: route
input:
  text: string
decide:
  model: openrouter/cloudflare/clef
  input: ${input.text}
  questions:
    urgent:
      ask: Does this need someone now?
`,
	"components/summarize/component.yaml": `name: summarize
input:
  ticket: object
  audience: string
ai:
  model: anthropic/m
  prompt: prompt.md
  input:
    ticket: ${input.ticket}
    for: ${input.audience}
  output: schema.json
tests:
  - name: writes a summary
    input:
      ticket: {subject: Dashboard is down}
      audience: engineers
    expect:
      output.summary: Dashboard is down.
      input.audience: engineers
  - name: expects too much
    input: case.json
    expect:
      output.summary: Something else.
      output.missing.field: x
`,
	"components/summarize/prompt.md":   "Summarize the ticket for its audience.",
	"components/summarize/schema.json": `{"type": "object", "required": ["summary"], "properties": {"summary": {"type": "string"}}}`,
	"components/summarize/case.json":   `{"ticket": {"subject": "Slow"}, "audience": "support"}`,
	"components/notify/component.yaml": `name: notify
input:
  url: string
  text: string
http:
  method: POST
  url: ${input.url}
  headers:
    Authorization: Bearer ${env.NOTIFY_TOKEN}
  body:
    text: ${input.text}
tests:
  - name: nothing is sent in a test
    input: {url: "http://unreachable.invalid/hook", text: hello}
    expect:
      output.status: 200
`,
}

func writeProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestStepsRunTheComponentsTheyUse(t *testing.T) {
	w, err := workflow.Load(writeProject(t, componentProject))
	if err != nil {
		t.Fatal(err)
	}
	p := &pager{status: http.StatusOK}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)

	model := &fakeModel{
		answer:   `{"summary": "Dashboard is down."}`,
		decision: map[string]any{"urgent": map[string]any{"value": true}},
	}
	var results []StepResult
	eng := &Engine{
		Model:  model,
		HTTP:   srv.Client(),
		Env:    map[string]string{"TEAM_URL": srv.URL + "/team", "NOTIFY_TOKEN": "t0ken"},
		OnStep: func(r StepResult) { results = append(results, r) },
	}
	trigger := map[string]any{"body": map[string]any{"subject": "Dashboard returns 502", "customer": "ops@example.org"}}
	if err := eng.Run(context.Background(), w, trigger); err != nil {
		t.Fatal(err)
	}

	if got, want := statuses(results), []string{"route=ok", "summary=ok", "tell=ok"}; !reflect.DeepEqual(got, want) {
		t.Errorf("statuses = %v, want %v", got, want)
	}
	if got := []string{results[0].Kind, results[1].Kind, results[2].Kind}; !reflect.DeepEqual(got, []string{"decide", "ai", "http"}) {
		t.Errorf("kinds = %v, want those of the components", got)
	}

	// Each component got what its step gave it, and read its own files.
	if model.gotDecide.State != "Dashboard returns 502" || model.gotDecide.Questions["urgent"].Kind != ai.YesNo {
		t.Errorf("the decision model got %+v", model.gotDecide)
	}
	if model.got.System != "Summarize the ticket for its audience." || model.got.Model != "anthropic/m" {
		t.Errorf("the model got model %q and prompt %q", model.got.Model, model.got.System)
	}
	if !strings.Contains(model.got.Input, `"for": "engineers"`) || !strings.Contains(model.got.Input, `"subject": "Dashboard returns 502"`) {
		t.Errorf("the model's input: %s", model.got.Input)
	}
	want := `POST /team application/json {"text":"Dashboard is down. (ops@example.org)"}`
	if len(p.bodies) != 1 || p.bodies[0] != want {
		t.Errorf("requests = %q, want one %q", p.bodies, want)
	}
	if out, _ := results[2].Output.(map[string]any); out["status"] != http.StatusOK {
		t.Errorf("the output of tell = %v", results[2].Output)
	}
}

func TestComponentRefusesInputOfTheWrongType(t *testing.T) {
	files := map[string]string{}
	for name, content := range componentProject {
		files[name] = content
	}
	// The value is only known when the step runs, so validation lets it by.
	files["workflow.yaml"] = strings.Replace(files["workflow.yaml"], "text: ${trigger.body.subject}", "text: ${trigger.body.priority}", 1)
	w, err := workflow.Load(writeProject(t, files))
	if err != nil {
		t.Fatal(err)
	}
	eng := &Engine{Model: &fakeModel{}, Env: map[string]string{"TEAM_URL": "http://unused", "NOTIFY_TOKEN": "t"}}
	err = eng.Run(context.Background(), w, map[string]any{"body": map[string]any{"priority": 3}})
	if want := `step "route" failed: input of route: text must be a string, but is a number`; err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}

func TestComponentIsTestedOnItsOwn(t *testing.T) {
	dir := writeProject(t, componentProject)
	summarize, err := workflow.LoadComponent(filepath.Join(dir, "components", "summarize"))
	if err != nil {
		t.Fatal(err)
	}
	eng := &Engine{Model: &fakeModel{answer: `{"summary": "Dashboard is down."}`}}

	res, err := eng.TestComponent(context.Background(), summarize, summarize.Tests[0])
	if err != nil || len(res.Failures) != 0 || res.Name != "writes a summary" || len(res.Steps) != 1 || res.Steps[0].Kind != "ai" {
		t.Errorf("first test: %+v, %v", res, err)
	}
	res, err = eng.TestComponent(context.Background(), summarize, summarize.Tests[1])
	want := []string{
		"output.missing.field: no such key: missing",
		`output.summary: expected "Something else.", got "Dashboard is down."`,
	}
	if err != nil || !reflect.DeepEqual(res.Failures, want) {
		t.Errorf("second test: failures %q, %v; want %q", res.Failures, err, want)
	}

	// An answer that breaks the component's schema fails the test.
	bad := &Engine{Model: &fakeModel{answer: `{"summary": 5}`}}
	res, err = bad.TestComponent(context.Background(), summarize, summarize.Tests[0])
	if err != nil || len(res.Failures) != 1 || !strings.Contains(res.Failures[0], "does not match schema.json") {
		t.Errorf("answer off the schema: %+v, %v", res, err)
	}

	// A model that cannot be reached says nothing about the component.
	down := &Engine{Model: &fakeModel{err: &ai.UnavailableError{Err: os.ErrDeadlineExceeded}}}
	if _, err := down.TestComponent(context.Background(), summarize, summarize.Tests[0]); err == nil {
		t.Error("a model that is unavailable did not stop the test")
	}

	// An http component is not sent, and needs no environment.
	notify, err := workflow.LoadComponent(filepath.Join(dir, "components", "notify"))
	if err != nil {
		t.Fatal(err)
	}
	res, err = eng.TestComponent(context.Background(), notify, notify.Tests[0])
	if err != nil || len(res.Failures) != 0 || res.Steps[0].Detail != "POST, not sent" {
		t.Errorf("http component: %+v, %v", res, err)
	}
}

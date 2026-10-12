package engine

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"aia/internal/workflow"
)

var endpointProject = map[string]string{
	"workflow.yaml": `name: moderate
trigger:
  webhook: /moderate
  input: comment.json
steps:
  - id: check
    decide:
      model: openrouter/cloudflare/clef
      input: ${trigger.body.text}
      questions:
        allowed:
          ask: Can this be published?
  - id: report
    when: "!check.allowed.value"
    http: {method: POST, url: "${env.REPORT_URL}", body: {text: "${trigger.body.text}"}}
respond:
  status: 201
  headers:
    X-Checked-By: ${env.NAME} for ${trigger.body.author}
  body:
    allowed: ${check.allowed.value}
    reported: ${report != null}
tests:
  - name: answers the caller
    fixture: ok.json
    expect:
      report: skipped
      response.status: 201
      response.body.allowed: true
      response.body.reported: false
      response.headers["X-Checked-By"]: test for dana
  - name: expects another answer
    fixture: ok.json
    expect:
      response.status: 200
      response.body.reason: spam
`,
	"comment.json": `{"type": "object", "required": ["text"], "properties": {"text": {"type": "string"}}}`,
	"ok.json":      `{"body": {"text": "Nice work.", "author": "dana"}}`,
}

func loadEndpoint(t *testing.T, change ...string) *workflow.Workflow {
	t.Helper()
	files := map[string]string{}
	for name, content := range endpointProject {
		files[name] = content
	}
	for i := 0; i+1 < len(change); i += 2 {
		if !strings.Contains(files["workflow.yaml"], change[i]) {
			t.Fatalf("the workflow does not contain %q", change[i])
		}
		files["workflow.yaml"] = strings.Replace(files["workflow.yaml"], change[i], change[i+1], 1)
	}
	w, err := workflow.Load(writeProject(t, files))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func allowed(yes bool) *fakeModel {
	return &fakeModel{decision: map[string]any{"allowed": map[string]any{"value": yes}}}
}

var comment = map[string]any{"body": map[string]any{"text": "Nice work.", "author": "dana"}}

func TestRunWorksOutTheResponse(t *testing.T) {
	eng := &Engine{Model: allowed(true), Env: map[string]string{"NAME": "aia", "REPORT_URL": "http://unused"}}
	resp, err := eng.Run(context.Background(), loadEndpoint(t), comment)
	if err != nil {
		t.Fatal(err)
	}
	want := &Response{
		Status:  201,
		Headers: map[string]string{"X-Checked-By": "aia for dana"},
		Body:    map[string]any{"allowed": true, "reported": false},
	}
	if !reflect.DeepEqual(resp, want) {
		t.Errorf("response = %+v, want %+v", resp, want)
	}

	// Without a status the answer is 200, and without respond there is none.
	resp, err = eng.Run(context.Background(), loadEndpoint(t, "  status: 201\n", ""), comment)
	if err != nil || resp.Status != 200 {
		t.Errorf("without a status: %+v, %v", resp, err)
	}
	old, err := workflow.Load("testdata/triage-ticket")
	if err != nil {
		t.Fatal(err)
	}
	quiet := &Engine{Model: &fakeModel{answer: refundLow}, Env: map[string]string{"PAGER_URL": "http://unused"}}
	if resp, err := quiet.Run(context.Background(), old, map[string]any{"body": "x"}); err != nil || resp != nil {
		t.Errorf("a workflow without respond: %+v, %v", resp, err)
	}
}

func TestRunFailsIfTheResponseCannotBeWorkedOut(t *testing.T) {
	// report is skipped, so it has no status to read.
	w := loadEndpoint(t, "reported: ${report != null}", "reported: ${report.status}")
	eng := &Engine{Model: allowed(true), Env: map[string]string{"NAME": "aia", "REPORT_URL": "http://unused"}}
	resp, err := eng.Run(context.Background(), w, comment)
	if resp != nil || err == nil || !strings.HasPrefix(err.Error(), "respond: body: ${report.status}: ") {
		t.Errorf("response %+v, error %v", resp, err)
	}
}

func TestRunRefusesABodyTheWorkflowDoesNotTake(t *testing.T) {
	model := allowed(true)
	var ran []StepResult
	eng := &Engine{Model: model, Env: map[string]string{"NAME": "aia"}, OnStep: func(r StepResult) { ran = append(ran, r) }}
	_, err := eng.Run(context.Background(), loadEndpoint(t), map[string]any{"body": map[string]any{"text": 5}})
	var input *workflow.InputError
	if !errors.As(err, &input) || len(ran) != 0 || model.gotDecide.Questions != nil {
		t.Errorf("error = %v after %d steps, want the body refused before any step", err, len(ran))
	}
}

func TestTestsCanExpectThingsOfTheResponse(t *testing.T) {
	w := loadEndpoint(t)
	eng := &Engine{Model: allowed(true), Env: map[string]string{"NAME": "test"}}

	res, err := eng.Test(context.Background(), w, w.Tests[0])
	if err != nil || len(res.Failures) != 0 {
		t.Errorf("first test: failures %q, %v", res.Failures, err)
	}
	res, err = eng.Test(context.Background(), w, w.Tests[1])
	want := []string{
		"response.body.reason: no such key: reason",
		"response.status: expected 200, got 201",
	}
	if err != nil || !reflect.DeepEqual(res.Failures, want) {
		t.Errorf("second test: failures %q, %v; want %q", res.Failures, err, want)
	}
}

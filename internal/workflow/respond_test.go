package workflow

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const endpoint = `name: x
trigger:
  webhook: /check
  input: in.json
steps:
  - id: a
    ai: {model: anthropic/m, prompt: p.md, input: "${trigger.body.text}", output: s.json}
  - id: b
    when: a.category == "refund"
    http: {method: POST, url: "${env.URL}"}
respond:
  status: 201
  headers:
    X-Team: ${env.TEAM}
  body:
    category: ${a.category}
    told: ${b != null}
tests:
  - fixture: good.json
    expect:
      response.status: 201
      response.body.category: refund
`

// endpointProject writes a workflow with the files the endpoint above names.
func endpointProject(t *testing.T, workflow string) string {
	t.Helper()
	return writeFiles(t, writeProject(t, workflow), map[string]string{
		"in.json":   `{"type": "object", "required": ["text"], "properties": {"text": {"type": "string", "minLength": 2}}}`,
		"good.json": `{"body": {"text": "hello"}}`,
		"bad.json":  `{"body": {"text": 5}}`,
	})
}

func TestEndpoint(t *testing.T) {
	w, err := Load(endpointProject(t, endpoint))
	if err != nil {
		t.Fatal(err)
	}
	if w.Trigger.Input != "in.json" || w.Respond.Status != 201 || w.Respond.Headers["X-Team"] != "${env.TEAM}" {
		t.Errorf("loaded trigger %+v, respond %+v", w.Trigger, w.Respond)
	}
	// What the answer reads from the environment, the workflow needs.
	if got, want := w.EnvVars(), []string{"TEAM", "URL"}; !reflect.DeepEqual(got, want) {
		t.Errorf("EnvVars = %v, want %v", got, want)
	}
	if got, want := w.RespondReads(), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("RespondReads = %v, want %v", got, want)
	}

	if err := w.CheckTrigger(map[string]any{"body": map[string]any{"text": "hello", "more": true}}); err != nil {
		t.Errorf("a body that matches: %v", err)
	}
	for _, tt := range []struct {
		body any
		want string
	}{
		{map[string]any{"text": 5}, "/text: "},
		{map[string]any{"text": "x"}, "/text: "},
		{map[string]any{}, "the body: missing property 'text'"},
		{"just text", "the body: "},
		{nil, "the body: "},
	} {
		var input *InputError
		err := w.CheckTrigger(map[string]any{"body": tt.body})
		if !errors.As(err, &input) || len(input.Problems) != 1 || !strings.HasPrefix(input.Problems[0], tt.want) {
			t.Errorf("body %v: error = %v, want one problem starting %q", tt.body, err, tt.want)
		}
	}

	// A workflow that names no schema takes anything.
	plain, err := Load(writeProject(t, "name: x\ntrigger: {webhook: /h}\nsteps:\n  - id: a\n    http: {method: GET, url: \"http://x\"}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := plain.CheckTrigger(map[string]any{"body": 5}); err != nil || len(plain.RespondReads()) != 0 {
		t.Errorf("without a schema: %v, reads %v", err, plain.RespondReads())
	}
}

func TestEndpointProblems(t *testing.T) {
	tests := []struct {
		name string
		old  string
		new  string
		want []string
	}{
		{"valid", "", "", nil},
		{"no caller to answer", "  webhook: /check\n  input: in.json", "  cron: 0 3 * * *",
			[]string{`10: respond: only a workflow started by a webhook has a caller to answer`}},
		{"input without a webhook", "  webhook: /check\n", "  manual: true\n", []string{
			`4: trigger: input describes the body of a webhook request, so it needs webhook`,
			`11: respond: only a workflow started by a webhook has a caller to answer`,
		}},
		{"schema file missing", "input: in.json", "input: nowhere.json", []string{`4: trigger: schema file not found: nowhere.json`}},
		{"schema not valid", "input: in.json", "input: bad.json", []string{`4: trigger: bad.json is not a valid JSON Schema`}},
		{"status", "status: 201", "status: 99", []string{`12: respond: status must be an HTTP status from 200 to 599`}},
		{"header", "X-Team: ${env.TEAM}", "X-Team: ${c.team}", []string{`14: respond: headers.X-Team: ${c.team}: there is nothing called c here; an expression here can read trigger, a, b and env`}},
		{"body", "category: ${a.category}", "category: ${a.}", []string{`15: respond: body: ${a.}: the expression ends too soon, after "a."`}},
		{"a step called response", "  - id: b", "  - id: response", []string{
			`8: step "response": this id is reserved`,
			`15: respond: body: ${b != null}: there is nothing called b here; an expression here can read trigger, a and env`,
		}},
		{"fixture the workflow would refuse", "fixture: good.json", "fixture: bad.json",
			[]string{`19: test 1: fixture bad.json does not match trigger.input: /text: got number, want string`}},
		{"expecting something of an answer there is none of", "respond:\n  status: 201\n  headers:\n    X-Team: ${env.TEAM}\n  body:\n    category: ${a.category}\n    told: ${b != null}\n", "",
			[]string{`15: test 1: expect.response.body.category: there is nothing called response here; an expression here can read trigger, a, b and env`, `14: test 1: expect.response.status: there is nothing called response here; an expression here can read trigger, a, b and env`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text := endpoint
			if tt.old != "" {
				if !strings.Contains(text, tt.old) {
					t.Fatalf("the workflow does not contain %q", tt.old)
				}
				text = strings.Replace(text, tt.old, tt.new, 1)
			}
			// writeProject provides bad.json as a schema that is not valid; here
			// it is also wanted as a fixture, so that case gets its own file.
			dir := endpointProject(t, text)
			if tt.name == "schema not valid" {
				writeFiles(t, dir, map[string]string{"bad.json": `{"type": 5}`})
			}
			_, err := Load(dir)
			checkProblems(t, err, tt.want)
		})
	}
}

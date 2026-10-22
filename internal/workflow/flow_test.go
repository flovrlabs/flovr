package workflow

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const reply = `name: reply
description: Drafts a reply and sends it.
input:
  ticket: object
  url: string
steps:
  - id: draft
    ai: {model: anthropic/m, prompt: prompt.md, input: "${input.ticket}", output: schema.json}
  - id: send
    when: draft.summary != ""
    use: ../notify
    with:
      url: ${input.url}
      text: ${draft.summary}
      urgent: false
output:
  subject: ${draft.summary}
  sent: ${send != null}
tests:
  - name: sends what it drafts
    input: {ticket: {subject: Down}, url: "http://x"}
    expect:
      send: called
      draft.summary != "": true
      output.sent: true
`

// flowProject writes a workflow next to a reply component made of steps,
// which uses the notify component, and returns the reply's file.
func flowProject(t *testing.T, workflow, component string) (dir, file string) {
	t.Helper()
	dir = project(t, workflow)
	writeFiles(t, dir, map[string]string{
		"components/reply/component.yaml": component,
		"components/reply/prompt.md":      "Draft a reply.",
		"components/reply/schema.json":    `{"type": "object", "properties": {"summary": {"type": "string"}}}`,
	})
	return dir, filepath.Join(dir, "components", "reply", ComponentFile)
}

const usesReply = `name: x
trigger: {webhook: /h}
steps:
  - id: answer
    use: components/reply
    with: {ticket: "${trigger.body}", url: "${env.MAIL_URL}"}
  - id: after
    when: answer.subject != "" && answer.sent
    http: {method: GET, url: "http://x"}
`

func TestComponentMadeOfSteps(t *testing.T) {
	dir, file := flowProject(t, usesReply, reply)
	c, err := LoadComponent(file)
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind() != "flow" || len(c.Steps) != 2 || c.Steps[1].Component() == nil || c.Steps[1].Component().Name != "notify" {
		t.Fatalf("loaded %+v", c)
	}
	notifyFile := filepath.Join(dir, "components", "notify", ComponentFile)
	if got, want := c.Files(), []string{file, notifyFile}; !reflect.DeepEqual(got, want) {
		t.Errorf("Files = %v, want %v", got, want)
	}
	// What the component it uses reads from the environment, it needs too.
	if got, want := c.EnvVars(), []string{"NOTIFY_TOKEN"}; !reflect.DeepEqual(got, want) {
		t.Errorf("EnvVars = %v, want %v", got, want)
	}

	w, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if w.Steps[0].Kind() != "flow" {
		t.Errorf("kind of the step that uses it = %q", w.Steps[0].Kind())
	}
	if got, want := w.Files(), []string{notifyFile, file}; !reflect.DeepEqual(got, want) {
		t.Errorf("the workflow's Files = %v, want %v", got, want)
	}
	if got, want := w.EnvVars(), []string{"MAIL_URL", "NOTIFY_TOKEN"}; !reflect.DeepEqual(got, want) {
		t.Errorf("the workflow's EnvVars = %v, want %v", got, want)
	}
	// Later steps can read what its output gives back, and nothing else.
	if got, want := w.Outputs("answer"), []string{"sent", "subject"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Outputs = %v, want %v", got, want)
	}
}

func TestFlowProblems(t *testing.T) {
	tests := []struct {
		name string
		old  string
		new  string
		want []string
	}{
		{"valid", "", "", nil},
		{"nothing given back", "output:\n  subject: ${draft.summary}\n  sent: ${send != null}\n", "", []string{
			`1: component "reply": output is required: it is what the component gives back to the step that uses it`,
		}},
		{"what the workflow has is out of reach", `input: "${input.ticket}"`, `input: "${trigger.body}"`, []string{
			`8: step "draft": ai.input: ${trigger.body}: there is nothing called trigger here; an expression here can read input and env`,
		}},
		{"an input that is not listed", "url: ${input.url}", "url: ${input.address}", []string{
			`6: component "reply": reads input.address, which is not listed under input`,
		}},
		{"a step read before it has run", `when: draft.summary != ""`, `when: send.status == 200`, []string{
			`10: step "send": when: send has not run yet here; a step can read only the steps before it`,
		}},
		{"a field a step does not put out", "subject: ${draft.summary}", "subject: ${draft.subject}", []string{
			`16: component "reply": output: draft has no subject; it has summary`,
		}},
		{"a step named as what steps are given", "  - id: draft", "  - id: input", []string{
			`7: step "input": this id is reserved`,
			`10: step "send": when: there is nothing called draft here; an expression here can read input and env`,
			`14: step "send": with.text: ${draft.summary}: there is nothing called draft here; an expression here can read input and env`,
			`16: component "reply": output: ${draft.summary}: there is nothing called draft here; an expression here can read input, send and env`,
			`24: test "sends what it drafts": expect.draft.summary != "": there is nothing called draft here; an expression here can read input, output, send and env`,
		}},
		{"what a test expects of a step", "send: called", "send: maybe", []string{
			`23: test "sends what it drafts": expect.send must be called or skipped`,
		}},
		{"no steps", "steps:\n  - id: draft\n    ai: {model: anthropic/m, prompt: prompt.md, input: \"${input.ticket}\", output: schema.json}\n  - id: send\n    when: draft.summary != \"\"\n    use: ../notify\n    with:\n      url: ${input.url}\n      text: ${draft.summary}\n      urgent: false\n", "steps: []\n", []string{
			`6: component "reply": steps needs at least one step`,
			`7: component "reply": output: ${send != null}: there is nothing called send here; an expression here can read input and env`,
			`14: test "sends what it drafts": expect.send: there is nothing called send here; an expression here can read input, output and env`,
			`15: test "sends what it drafts": expect.draft.summary != "": there is nothing called draft here; an expression here can read input, output and env`,
		}},
		{"steps and work of its own", "steps:\n", "http: {method: GET, url: \"http://x\"}\nsteps:\n", []string{
			`1: component "reply": a component can have only one of ai, decide, http, steps`,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text := reply
			if tt.old != "" {
				if !strings.Contains(text, tt.old) {
					t.Fatalf("the component does not contain %q", tt.old)
				}
				text = strings.Replace(text, tt.old, tt.new, 1)
			}
			_, file := flowProject(t, usesReply, text)
			_, err := LoadComponent(file)
			checkProblems(t, err, tt.want)
		})
	}
}

func TestOutputIsOnlyForAComponentMadeOfSteps(t *testing.T) {
	files := map[string]string{}
	for name, content := range summarizeFiles {
		files[name] = content
	}
	files[ComponentFile] = strings.Replace(summarize, "tests:\n", "output: {summary: \"${input.audience}\"}\ntests:\n", 1)
	_, err := LoadComponent(writeFiles(t, t.TempDir(), files))
	checkProblems(t, err, []string{`12: component "summarize": output is for a component made of steps; this one gives back what its ai puts out`})
}

func TestComponentCannotUseItself(t *testing.T) {
	dir := t.TempDir()
	flow := func(name, uses string) string {
		return "name: " + name + "\ninput: {n: number}\nsteps:\n  - id: again\n    use: ../" + uses + "\n    with: {n: \"${input.n}\"}\noutput: {n: \"${input.n}\"}\n"
	}
	writeFiles(t, dir, map[string]string{
		"a/component.yaml": flow("a", "b"),
		"b/component.yaml": flow("b", "c"),
		"c/component.yaml": flow("c", "a"),
		"d/component.yaml": flow("d", "d"),
	})
	for name, want := range map[string]string{
		"a": `the component ../b has a problem`,
		"d": `the component ../d would use itself, which never ends`,
	} {
		_, err := LoadComponent(filepath.Join(dir, name))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error = %v, want one containing %q", name, err, want)
		}
	}
	// The reason is carried up from where the circle closes.
	if _, err := LoadComponent(filepath.Join(dir, "a")); err == nil || !strings.Contains(err.Error(), "would use itself") {
		t.Errorf("a: error = %v, want it to say where the circle closes", err)
	}
}

func TestWorkflowReadsOnlyWhatAFlowGivesBack(t *testing.T) {
	dir, _ := flowProject(t, strings.Replace(usesReply, "answer.sent", "answer.draft.summary", 1), reply)
	_, err := Load(dir)
	checkProblems(t, err, []string{`7: step "after": answer has no draft; it has sent, subject`})
}

package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeFiles writes files, given by path below dir, and returns dir.
func writeFiles(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
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

const summarize = `name: summarize
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
  - name: says something
    input:
      ticket: {subject: Down}
      audience: engineers
    expect:
      output.summary != "": true
  - input: case.json
    expect:
      input.audience: support
`

var summarizeFiles = map[string]string{
	"component.yaml": summarize,
	"prompt.md":      "Summarize the ticket.",
	"schema.json":    `{"type": "object", "properties": {"summary": {"type": "string"}}}`,
	"case.json":      `{"ticket": {"subject": "Slow"}, "audience": "support"}`,
}

func TestLoadComponent(t *testing.T) {
	dir := writeFiles(t, t.TempDir(), summarizeFiles)
	for _, path := range []string{dir, filepath.Join(dir, ComponentFile)} {
		if !IsComponent(path) {
			t.Errorf("IsComponent(%s) = false", path)
		}
		c, err := LoadComponent(path)
		if err != nil {
			t.Fatal(err)
		}
		if c.Name != "summarize" || c.Kind() != "ai" || c.Path != filepath.Join(dir, ComponentFile) || len(c.Tests) != 2 {
			t.Errorf("loaded %+v", c)
		}
		if want := (Params{{"ticket", "object"}, {"audience", "string"}}); !reflect.DeepEqual(c.Input, want) {
			t.Errorf("inputs = %v, want %v in the order written", c.Input, want)
		}
		if got := c.Resolve("prompt.md"); got != filepath.Join(dir, "prompt.md") {
			t.Errorf("Resolve = %s", got)
		}
		values, err := c.Values(c.Tests[1])
		if err != nil || values["audience"] != "support" {
			t.Errorf("the input of the second test = %v, %v", values, err)
		}
	}

	// A directory with a workflow file is a workflow, whatever else it holds.
	both := writeFiles(t, t.TempDir(), map[string]string{FileName: "name: w\n", ComponentFile: "name: c\n"})
	if IsComponent(both) || IsComponent(t.TempDir()) || IsComponent(filepath.Join(both, FileName)) {
		t.Error("IsComponent is true for something that is not a component")
	}
}

func TestComponentProblems(t *testing.T) {
	tests := []struct {
		name      string
		component string
		want      []string
	}{
		{
			name: "an http component",
			component: `name: notify
input: {url: string, text: string}
http:
  method: POST
  url: ${input.url}
  headers: {Authorization: "Bearer ${env.TOKEN}"}
  body: {text: "${input.text}"}
`,
		},
		{
			name:      "nothing in it",
			component: "input: {a: string}\n",
			want:      []string{`1: name is required`, `1: component: needs one of ai, decide, http`},
		},
		{
			name: "two kinds of work",
			component: `name: x
http: {method: GET, url: "http://x"}
decide: {model: openrouter/m, input: hi, questions: {q: {ask: "Is it?"}}}
`,
			want: []string{`1: component "x": a component can have only one of ai, decide, http`},
		},
		{
			name: "inputs",
			component: `name: x
input:
  good: string
  2bad: string
  odd: text
  good: number
http: {method: GET, url: "http://x"}
`,
			want: []string{
				`4: component "x": the input "2bad" may contain only letters, digits and underscores, and must not start with a digit`,
				`5: component "x": the type of input "odd" must be one of string, number, boolean, object, list, any`,
				`3: component "x": the input "good" is listed twice`,
			},
		},
		{
			name:      "inputs that are not a mapping",
			component: "name: x\ninput: [a, b]\nhttp: {method: GET, url: \"http://x\"}\n",
			want:      []string{`2: input must list what the component takes, as name: type`},
		},
		{
			name: "what the workflow has is out of reach",
			component: `name: x
input: {url: string}
http:
  method: GET
  url: ${input.url}/${trigger.body.id}
  body: ${input.text}
`,
			want: []string{
				`5: component "x": http.url: ${trigger.body.id}: undeclared reference to 'trigger'`,
				`3: component "x": reads input.text, which is not listed under input`,
			},
		},
		{
			name: "files are looked up next to the component",
			component: `name: x
input: {t: string}
ai: {model: anthropic/m, prompt: missing.md, input: "${input.t}", output: schema.json}
`,
			want: []string{`3: component "x": prompt file not found: missing.md`},
		},
		{
			name: "tests",
			component: `name: x
input: {t: string}
ai: {model: anthropic/m, prompt: prompt.md, input: "${input.t}", output: schema.json}
tests:
  - name: no input
    expect: {output.summary: x}
  - name: wrong input
    input: {t: 5}
  - name: missing file
    input: nowhere.json
  - name: not by name
    input: [a]
  - input: {t: hello}
    expect:
      output.: x
      trigger.body: x
`,
			want: []string{
				`5: test "no input": input is required`,
				`8: test "wrong input": input: t must be a string, but is a number`,
				`10: test "missing file": input file not found: nowhere.json`,
				`12: test "not by name": input must hold a value for each of the component's inputs, by name`,
				`15: test 5: expect.output.: Syntax error: no viable alternative at input '.'`,
				`16: test 5: expect.trigger.body: undeclared reference to 'trigger'`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{}
			for name, content := range summarizeFiles {
				files[name] = content
			}
			files[ComponentFile] = tt.component
			_, err := LoadComponent(writeFiles(t, t.TempDir(), files))
			checkProblems(t, err, tt.want)
		})
	}
}

// checkProblems compares the problems err holds with want, each written as
// "line: message", in any order.
func checkProblems(t *testing.T, err error, want []string) {
	t.Helper()
	var got []string
	var problems *Problems
	switch {
	case errors.As(err, &problems):
		for _, p := range problems.Items {
			got = append(got, strings.TrimSpace(strings.SplitN(p.Msg, "\n", 2)[0]))
			got[len(got)-1] = itoa(p.Line) + ": " + got[len(got)-1]
		}
	case err != nil:
		t.Fatal(err)
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			found = found || strings.HasPrefix(g, w)
		}
		if !found {
			t.Errorf("missing problem %q", w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d problems, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for ; n > 0; n /= 10 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
	}
	return string(digits)
}

const notify = `name: notify
input:
  url: string
  text: string
  urgent: boolean
http:
  method: POST
  url: ${input.url}
  headers:
    Authorization: Bearer ${env.NOTIFY_TOKEN}
  body: {text: "${input.text}", urgent: "${input.urgent}"}
`

// project writes a workflow next to the summarize and notify components.
func project(t *testing.T, workflow string) string {
	t.Helper()
	dir := writeProject(t, workflow)
	for name, content := range summarizeFiles {
		writeFiles(t, dir, map[string]string{filepath.Join("components", "summarize", name): content})
	}
	return writeFiles(t, dir, map[string]string{
		"components/notify/component.yaml": notify,
		"components/broken/component.yaml": "name: broken\ninput: {a: text}\n",
	})
}

func TestWorkflowUsesComponents(t *testing.T) {
	dir := project(t, `name: x
trigger:
  webhook: /hook
steps:
  - id: summary
    use: components/summarize
    with:
      ticket: ${trigger.body}
      audience: engineers
  - id: tell
    when: summary.summary != ""
    use: components/notify/component.yaml
    with:
      url: ${env.TEAM_URL}
      text: "Ticket: ${summary.summary}"
      urgent: ${trigger.body.urgent}
  - id: tell_again
    use: components/notify
    with: {url: "${env.OTHER_URL}", text: again, urgent: false}
`)
	w, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{w.Steps[0].Kind(), w.Steps[1].Kind(), w.Steps[2].Kind()}; !reflect.DeepEqual(got, []string{"ai", "http", "http"}) {
		t.Errorf("kinds = %v", got)
	}
	if c := w.Steps[1].Component(); c == nil || c.Name != "notify" || c != w.Steps[2].Component() {
		t.Errorf("the two steps that use notify do not share the component: %v", c)
	}
	// What a component reads from the environment, the workflow needs too.
	if got, want := w.EnvVars(), []string{"NOTIFY_TOKEN", "OTHER_URL", "TEAM_URL"}; !reflect.DeepEqual(got, want) {
		t.Errorf("EnvVars = %v, want %v", got, want)
	}
	want := []string{
		filepath.Join(dir, "components", "notify", ComponentFile),
		filepath.Join(dir, "components", "summarize", ComponentFile),
	}
	if got := w.Files(); !reflect.DeepEqual(got, want) {
		t.Errorf("Files = %v, want %v", got, want)
	}
	if got := w.Reads(1); !reflect.DeepEqual(got, []string{"trigger", "summary"}) {
		t.Errorf("Reads(1) = %v", got)
	}
}

func TestUseProblems(t *testing.T) {
	tests := []struct {
		name string
		step string
		want []string
	}{
		{
			name: "no such component",
			step: "use: components/missing\n    with: {a: b}",
			want: []string{`4: step "a": component not found: components/missing`},
		},
		{
			name: "a component with a problem",
			step: "use: components/broken",
			want: []string{`4: step "a": the component components/broken has a problem: `},
		},
		{
			name: "what is given does not fit what it takes",
			step: `use: components/notify
    with:
      url: 5
      text: ${trigger.body.}
      urgent: "it is ${trigger.body.level}"
      extra: x`,
			want: []string{
				`6: step "a": with.url must be a string, but is a number`,
				`7: step "a": with.text: ${trigger.body.}: Syntax error`,
				`8: step "a": with.urgent must be a boolean, but is a string`,
				`9: step "a": with.extra is not an input of notify`,
			},
		},
		{
			name: "something is not given",
			step: "use: components/notify\n    with: {url: \"http://x\"}",
			want: []string{
				`5: step "a": with.text is missing; notify takes it as input`,
				`5: step "a": with.urgent is missing; notify takes it as input`,
			},
		},
		{
			name: "two kinds of work",
			step: "use: components/notify\n    http: {method: GET, url: \"http://x\"}",
			want: []string{`3: step "a": a step can have only one of ai, decide, http, use`},
		},
		{
			name: "with and no use",
			step: "http: {method: GET, url: \"http://x\"}\n    with: {a: b}",
			want: []string{`5: step "a": with is only for a step that has use`},
		},
		{
			name: "nothing to do",
			step: "when: \"true\"",
			want: []string{`3: step "a": needs one of ai, decide, http, use`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(project(t, "name: x\nsteps:\n  - id: a\n    "+tt.step+"\n"))
			checkProblems(t, err, tt.want)
		})
	}
}

func TestParamsCheck(t *testing.T) {
	params := Params{{"url", "string"}, {"count", "number"}, {"on", "boolean"}, {"ticket", "object"}, {"tags", "list"}, {"extra", "any"}}
	good := map[string]any{"url": "http://x", "count": 2.5, "on": true, "ticket": map[string]any{}, "tags": []any{"a"}, "extra": nil}
	if err := params.Check(good); err != nil {
		t.Errorf("good values: %v", err)
	}
	for change, want := range map[string]string{
		"url=5":       "url must be a string, but is a number",
		"count=x":     "count must be a number, but is a string",
		"ticket=null": "ticket must be an object, but is empty",
		"tags=x":      "tags must be a list, but is a string",
		"-on":         "on is missing",
		"+more":       "more is not one of the inputs",
	} {
		values := map[string]any{}
		for k, v := range good {
			values[k] = v
		}
		switch change {
		case "url=5":
			values["url"] = 5
		case "count=x":
			values["count"] = "x"
		case "ticket=null":
			values["ticket"] = nil
		case "tags=x":
			values["tags"] = "x"
		case "-on":
			delete(values, "on")
		case "+more":
			values["more"] = 1
		}
		if err := params.Check(values); err == nil || err.Error() != want {
			t.Errorf("%s: error = %v, want %q", change, err, want)
		}
	}
}

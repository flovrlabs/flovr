package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/flovrlabs/flovr/internal/ai"
)

func TestLoadExample(t *testing.T) {
	w, err := Load("../../examples/triage-ticket")
	if err != nil {
		t.Fatal(err)
	}
	if w.Name != "triage-ticket" || len(w.Steps) != 4 || len(w.Tests) != 3 {
		t.Fatalf("loaded %q with %d steps and %d tests", w.Name, len(w.Steps), len(w.Tests))
	}
	if got := []string{w.Steps[0].Kind(), w.Steps[1].Kind(), w.Steps[2].Kind(), w.Steps[3].Kind()}; !reflect.DeepEqual(got, []string{"decide", "ai", "http", "http"}) {
		t.Errorf("step kinds = %q", got)
	}
}

func TestQuestions(t *testing.T) {
	w, err := Load(writeProject(t, `name: x
steps:
  - id: a
    decide:
      model: openrouter/vendor/m
      input: ${trigger.body}
      questions:
        category:
          ask: What is it about?
          choice:
            refund: They want money back.
            billing:
        team:
          ask: Who owns it?
          choice: [payments, platform: Infrastructure and uptime.]
        priority:
          ask: How urgent?
          score:
            - low
            - high: Cannot use the product.
        spam:
          ask: Is this spam?
        angry:
          ask: Is the customer angry?
          yes_if: Hostile wording.
          no_if: Neutral or polite wording.
`))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ai.Question{}
	for name, q := range w.Steps[0].Decide.Questions {
		got[name] = q.Spec()
	}
	want := map[string]ai.Question{
		"category": {Ask: "What is it about?", Kind: ai.Choice, Options: []ai.Option{{Name: "refund", Meaning: "They want money back."}, {Name: "billing", Meaning: ""}}},
		"team":     {Ask: "Who owns it?", Kind: ai.Choice, Options: []ai.Option{{Name: "payments", Meaning: ""}, {Name: "platform", Meaning: "Infrastructure and uptime."}}},
		"priority": {Ask: "How urgent?", Kind: ai.Score, Options: []ai.Option{{Name: "low", Meaning: ""}, {Name: "high", Meaning: "Cannot use the product."}}},
		"spam":     {Ask: "Is this spam?", Kind: ai.YesNo},
		"angry":    {Ask: "Is the customer angry?", Kind: ai.YesNo, YesIf: "Hostile wording.", NoIf: "Neutral or polite wording."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("questions:\n got %+v\nwant %+v", got, want)
	}
}

// writeProject creates a directory holding the given workflow file plus a
// prompt, a schema and a fixture for it to refer to.
func writeProject(t *testing.T, workflow string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"workflow.yaml": workflow,
		"p.md":          "Classify the ticket.",
		"s.json":        `{"type": "object", "properties": {"category": {"type": "string"}}}`,
		"bad.json":      `{"type": 5}`,
		"f.json":        `{"body": {}}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestProblems(t *testing.T) {
	tests := []struct {
		name     string
		workflow string
		want     []string
	}{
		{
			name: "valid",
			workflow: `name: ok
trigger:
  cron: "0 9 * * 1"
steps:
  - id: classify
    ai: {model: anthropic/m, prompt: p.md, input: "${trigger.body}", output: s.json}
  - id: notify
    when: classify.category == "refund"
    http:
      method: post
      url: ${env.URL}
      headers:
        Authorization: Bearer ${env.TOKEN}
      body: {category: "${classify.category}"}
tests:
  - fixture: f.json
    expect: {classify.category: refund, notify: called}
`,
		},
		{
			name: "unknown field",
			workflow: `name: x
steps:
  - id: a
    ai: {modle: m, prompt: p.md, input: hi, output: s.json}
`,
			want: []string{`4: unknown field "modle"`},
		},
		{
			name: "syntax error",
			workflow: `name: x
steps: [
`,
			want: []string{`2: did not find expected node content`},
		},
		{
			name:     "empty file",
			workflow: ``,
			want:     []string{`1: EOF`},
		},
		{
			name: "missing pieces",
			workflow: `trigger:
  webhook: tickets
  cron: every day
steps:
  - ai: {}
  - id: 2nd
    http: {method: SEND}
  - id: env
    http: {method: GET, url: "http://x"}
`,
			want: []string{
				`1: name is required`,
				`1: trigger: needs exactly one of webhook, cron, manual`,
				`2: trigger: webhook must be a path starting with /`,
				`3: trigger: cron "every day" is not a schedule of five fields (minute hour day month weekday)`,
				`5: step 1: id is required`,
				`5: step 1: ai.model is required`,
				`5: step 1: ai.prompt is required`,
				`5: step 1: ai.input is required`,
				`5: step 1: ai.output is required`,
				`6: step "2nd": id may contain only letters, digits and underscores, and must not start with a digit`,
				`7: step "2nd": http.method must be one of GET, POST, PUT, PATCH, DELETE`,
				`7: step "2nd": http.url is required`,
				`8: step "env": this id is reserved`,
			},
		},
		{
			name: "decide step",
			workflow: `name: x
steps:
  - id: a
    decide:
      model: anthropic/m
      questions:
        2nd:
          choice: [only]
        both:
          ask: Which?
          choice: {a: x, b: y}
          score: [low, low]
          yes_if: sure
        half:
          ask: Really?
          yes_if: sure
  - id: b
    when: a.half.value
    decide:
      model: openrouter/m
      input: ${a.both.value} ${nope}
      questions: {}
`,
			want: []string{
				`4: step "a": decide.input is required`,
				`5: step "a": decide.model must be a decision model, which come from openrouter`,
				`7: step "a": question "2nd": its name may contain only letters, digits and underscores, and must not start with a digit`,
				`7: step "a": question "2nd": ask is required`,
				`8: step "a": question "2nd": choice needs at least two answers`,
				`9: step "a": question "both": can have only one of choice, score`,
				`9: step "a": question "both": yes_if and no_if are only for yes/no questions, which have no choice or score`,
				`12: step "a": question "both": score lists "low" twice`,
				`14: step "a": question "half": yes_if and no_if must be given together`,
				`21: step "b": decide.input: ${nope}: there is nothing called nope here; an expression here can read trigger, a and env`,
				`22: step "b": decide.questions needs at least one question`,
			},
		},
		{
			name: "score written as a map",
			workflow: `name: x
steps:
  - id: a
    decide:
      model: openrouter/m
      input: hi
      questions:
        priority:
          ask: How urgent?
          score:
            low: Can wait.
            high: Cannot wait.
`,
			want: []string{`11: score must be a list, lowest level first, because a map does not keep its order`},
		},
		{
			name: "answers that are not names",
			workflow: `name: x
steps:
  - id: a
    decide:
      model: openrouter/m
      input: hi
      questions:
        team:
          ask: Who owns it?
          choice:
            - payments
            - {platform: Uptime., extra: Not allowed.}
`,
			want: []string{`12: expected a name or one name: meaning pair`},
		},
		{
			name: "files and references",
			workflow: `name: x
steps:
  - id: a
    when: later.done
    ai:
      model: m
      prompt: nope.md
      input: ${trigger.body
      output: bad.json
  - id: a
    http:
      method: GET
      url: http://x
  - id: later
    ai: {model: m, prompt: p.md, input: hi, output: nope.json}
    http: {method: GET, url: "http://x"}
tests:
  - name: t
    fixture: nope.json
    expect:
      b.category: refund
      a: maybe
      a.category ==: refund
`,
			want: []string{
				`4: step "a": when: later has not run yet here; a step can read only the steps before it`,
				`6: step "a": ai.model must be written as provider/model, where provider is one of anthropic, ollama, openai, openrouter`,
				`7: step "a": prompt file not found: nope.md`,
				`8: step "a": ai.input: unclosed ${ in "${trigger.body"`,
				`9: step "a": bad.json is not a valid JSON Schema:`,
				`10: step "a": this id is already used by an earlier step`,
				`14: step "later": a step can have only one of ai, decide, http`,
				`19: test "t": fixture file not found: nope.json`,
				`21: test "t": expect.b.category: there is nothing called b here; an expression here can read trigger, a, later and env`,
				`22: test "t": expect.a must be called or skipped`,
				`23: test "t": expect.a.category ==: the expression ends too soon, after "a.category =="`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeProject(t, tt.workflow))
			var got []string
			var problems *Problems
			if errors.As(err, &problems) {
				for _, p := range problems.Items {
					got = append(got, fmt.Sprintf("%d: %s", p.Line, p.Msg))
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d problems, want %d:\n%s", len(got), len(tt.want), join(got))
			}
			for i := range got {
				// A want may be a prefix, for messages that end in a library's own wording.
				if len(got[i]) < len(tt.want[i]) || got[i][:len(tt.want[i])] != tt.want[i] {
					t.Errorf("problem %d:\n got %s\nwant %s", i+1, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestEnvVars(t *testing.T) {
	w, err := Load(writeProject(t, `name: x
steps:
  - id: a
    when: env.MODE == "live" && has(env.REGION)
    ai: {model: openrouter/vendor/m, prompt: p.md, input: "${env.TENANT}: ${trigger.body}", output: s.json}
  - id: b
    http:
      method: POST
      url: ${env.URL}/v1
      headers:
        Authorization: Bearer ${env.TOKEN}
      body: {tenant: "${env.TENANT}", items: ["${a.category + env.SUFFIX}"]}
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"MODE", "REGION", "SUFFIX", "TENANT", "TOKEN", "URL"}
	if got := w.EnvVars(); !reflect.DeepEqual(got, want) {
		t.Errorf("EnvVars() = %v, want %v", got, want)
	}

	example, err := Load("../../examples/triage-ticket")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := example.EnvVars(), []string{"BILLING_URL", "PAGER_URL"}; !reflect.DeepEqual(got, want) {
		t.Errorf("example EnvVars() = %v, want %v", got, want)
	}
}

func TestProblemsError(t *testing.T) {
	p := &Problems{Path: "w.yaml", Items: []Problem{{3, "name is required"}}}
	if got, want := p.Error(), "w.yaml:3: name is required\n1 problem"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestResolve(t *testing.T) {
	w := &Workflow{Path: filepath.Join("flows", "triage", "workflow.yaml")}
	got := []string{w.Resolve("prompts/p.md"), w.Resolve("/abs/p.md")}
	want := []string{filepath.Join("flows", "triage", "prompts", "p.md"), "/abs/p.md"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Resolve = %v, want %v", got, want)
	}
}

func TestParseChecksContentThatIsNotInTheFileYet(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "triage-ticket", FileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The files it names are looked up next to the path it is given.
	if w, err := Parse(path, data); err != nil || w.Path != path || len(w.Steps) != 4 {
		t.Fatalf("Parse = %+v, %v", w, err)
	}
	changed := bytes.Replace(data, []byte("prompts/summarize.md"), []byte("prompts/missing.md"), 1)
	var problems *Problems
	if _, err := Parse(path, changed); !errors.As(err, &problems) || len(problems.Items) != 1 || problems.Items[0].Msg != `step "summarize": prompt file not found: prompts/missing.md` {
		t.Errorf("Parse of changed content: error = %v", err)
	}
}

func TestWritable(t *testing.T) {
	w, err := Load(writeProject(t, `name: x
trigger:
  webhook: /hook
steps:
  - id: a
    decide:
      model: openrouter/cloudflare/clef
      input: {ticket: "${trigger.body}"}
      questions:
        kind:
          ask: What is it?
          choice:
            bug: It is broken.
            other:
        level:
          ask: How bad?
          score:
            - low
            - high: Very.
`))
	if err != nil {
		t.Fatal(err)
	}
	questions := []any{"steps", 0, "decide", "questions"}
	at := func(path ...any) []any { return append(append([]any{}, questions...), path...) }
	for _, tt := range []struct {
		path []any
		want bool
	}{
		{[]any{"name"}, true},
		{[]any{"trigger", "webhook"}, true},
		{[]any{"steps", 0, "when"}, true}, // a key the step does not have yet
		{at("kind", "ask"), true},
		{at("kind", "choice", "bug"), true},
		{at("kind", "choice", "other"), true}, // an answer without a meaning
		{at("level", "score", 1, "high"), true},

		{[]any{"steps", 0, "decide", "input", "ticket"}, false}, // written inline
		{[]any{"steps", 0, "decide", "input"}, false},           // not text
		{at("level", "score", 0, "low"), false},                 // a bare name, with no meaning to change
		{at("level", "score", 1), false},                        // a list item
		{at("kind", "choice", 0, "bug"), false},
		{[]any{"steps", 3, "when"}, false},
		{[]any{"trigger", "webhook", "x"}, false},
		{nil, false},
	} {
		if got := w.Writable(tt.path...); got != tt.want {
			t.Errorf("Writable(%v) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func join(lines []string) string {
	out := ""
	for _, l := range lines {
		out += "  " + l + "\n"
	}
	return out
}

func TestDiscover(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"billing/refunds", "support", ".git/hooks", "support/testdata/case"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		content := "name: " + filepath.Base(dir) + "\nsteps:\n  - id: a\n    http: {method: GET, url: \"http://x\"}\n"
		if err := os.WriteFile(filepath.Join(root, dir, FileName), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	names := func(ws []*Workflow) (out []string) {
		for _, w := range ws {
			out = append(out, w.Name)
		}
		return out
	}

	found, err := Discover(root)
	if got, want := names(found), []string{"refunds", "support"}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Discover(root) = %v, %v; want %v, skipping hidden and testdata directories", got, err, want)
	}
	found, err = Discover(filepath.Join(root, "support", FileName), filepath.Join(root, "billing"))
	if got, want := names(found), []string{"support", "refunds"}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Discover(file, dir) = %v, %v; want %v", got, err, want)
	}

	empty := t.TempDir()
	if _, err := Discover(empty); err == nil || err.Error() != "no workflow.yaml found in "+empty {
		t.Errorf("Discover(empty dir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "support", FileName), []byte("name: broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var problems *Problems
	if _, err := Discover(root); !errors.As(err, &problems) {
		t.Errorf("Discover with an invalid workflow: error = %v, want its problems", err)
	}
}

// A folder may have a name that would mean something else in a URL.
func TestSchemaInAFolderWithAnOddName(t *testing.T) {
	for _, name := range []string{"plain", "with space", "issue #12", "50% off", "a?b"} {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "s.json"), []byte(`{"type": "object", "required": ["a"]}`), 0o644); err != nil {
			t.Fatal(err)
		}
		w := &Workflow{Path: filepath.Join(dir, FileName)}
		schema, doc, err := w.Schema("s.json")
		if err != nil || doc["type"] != "object" {
			t.Errorf("%q: %v", name, err)
			continue
		}
		if err := schema.Validate(map[string]any{}); err == nil {
			t.Errorf("%q: the schema does not check what it should", name)
		}
	}
}

package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

// fields is a workflow whose steps put out values of every kind. Each test
// adds one step, a respond or a test that reads from them.
const fields = `name: x
trigger:
  webhook: /hook
  input: in.json
steps:
  - id: sort
    decide:
      model: openrouter/cloudflare/clef
      input: ${trigger.body.text}
      questions:
        category:
          ask: What is it about?
          choice: {refund: Money back., other: Anything else.}
        priority:
          ask: How urgent?
          score: [low, high]
        allowed:
          ask: May it be published?
  - id: write
    ai: {model: anthropic/m, prompt: p.md, input: "${trigger.body.text}", output: s.json}
  - id: send
    http: {method: POST, url: "http://x"}
  - id: summary
    use: components/summarize
    with: {ticket: "${trigger.body}", audience: all}
`

func TestReadingAFieldThatIsNotThere(t *testing.T) {
	const line = 26 // where the added step starts
	tests := []struct {
		name string
		when string
		want string
	}{
		{"everything a decide step puts out", `sort.category.value == "refund" && sort.category.confidence > 0.5 && sort.category.probabilities.other < 0.1 && sort.priority.score > 1.0 && sort.priority.probabilities.high > 0.5 && sort.allowed.value && sort.allowed.probability > 0.5`, ""},
		{"a question that is not asked", `sort.priorty.value == "high"`, `sort has no priorty; it has allowed, category, priority`},
		{"a part an answer does not have", `sort.priority.valeu == "high"`, `sort.priority has no valeu; it has confidence, probabilities, score, value`},
		{"an answer that is not among the choices", `sort.category.probabilities.refnd > 0.5`, `sort.category.probabilities has no refnd; it has other, refund`},
		{"a score only a score question has", `sort.category.score > 1.0`, `sort.category has no score; it has confidence, probabilities, value`},
		{"a yes/no answer has no confidence", `sort.allowed.confidence > 0.5`, `sort.allowed has no confidence; it has probability, value`},

		{"what the schema of an ai step lists", `write.category == "refund"`, ""},
		{"what it does not list", `write.categry == "refund"`, `write has no categry; it has category`},
		{"a field of a piece of text", `write.category.name == "refund"`, `write.category is a single value, so there is no write.category.name`},

		{"what an http step puts out", `send.status == 200 && send.body.anything.at.all == 1`, ""},
		{"what an http step does not put out", `send.headers.x == "y"`, `send has no headers; it has body, status`},

		{"what a component puts out", `summary.summary != ""`, ""},
		{"what a component does not put out", `summary.text != ""`, `summary has no text; it has summary`},

		{"a body's property the schema lists", `trigger.body.text != ""`, ""},
		{"one it does not list, which a body may still carry", `trigger.body.author != ""`, ""},
		{"anything else of the trigger", `trigger.time != ""`, ""},
		{"a field that is only tested for", `has(write.missing)`, `write has no missing; it has category`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			step := "  - id: last\n    when: '" + strings.ReplaceAll(tt.when, "'", "''") + "'\n    http: {method: GET, url: \"http://x\"}\n"
			dir := writeFiles(t, project(t, fields+step), map[string]string{
				"in.json": `{"type": "object", "properties": {"text": {"type": "string"}}}`,
			})
			_, err := Load(dir)
			var want []string
			if tt.want != "" {
				want = []string{itoa(line) + `: step "last": ` + tt.want}
			}
			checkProblems(t, err, want)
		})
	}
}

func TestReadingAFieldThatIsNotThereElsewhere(t *testing.T) {
	tests := []struct {
		name string
		add  string
		in   string
		want []string
	}{
		{
			name: "a body whose schema allows nothing but what it lists",
			add:  "  - id: last\n    when: trigger.body.author != \"\"\n    http: {method: GET, url: \"http://x\"}\n",
			in:   `{"type": "object", "properties": {"text": {"type": "string"}}, "additionalProperties": false}`,
			want: []string{`26: step "last": trigger.body has no author; it has text`},
		},
		{
			name: "what a step gives a component, and where it sends",
			add:  "  - id: last\n    use: components/notify\n    with: {url: \"${send.body.url}/${send.code}\", text: \"${write.summary}\", urgent: false}\n",
			want: []string{
				`26: step "last": send has no code; it has body, status`,
				`26: step "last": write has no summary; it has category`,
			},
		},
		{
			name: "the answer to the caller",
			add:  "respond:\n  body:\n    category: ${sort.category.value}\n    urgent: ${sort.urgent.value}\n",
			want: []string{`26: respond: sort has no urgent; it has allowed, category, priority`},
		},
		{
			name: "what tests expect",
			add: "respond:\n  body: {category: \"${sort.category.value}\"}\ntests:\n  - fixture: f.json\n    expect:\n" +
				"      sort.category.value: refund\n      response.status: 200\n      response.body.category: refund\n" +
				"      response.body.reason: none\n      response.stats: 200\n      write.catgory: refund\n",
			in: `{"type": "object"}`,
			want: []string{
				`34: test 1: expect: response.body has no reason; it has category`,
				`35: test 1: expect: response has no stats; it has body, headers, status`,
				`36: test 1: expect: write has no catgory; it has category`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.in == "" {
				tt.in = `{"type": "object", "properties": {"text": {"type": "string"}}}`
			}
			dir := writeFiles(t, project(t, fields+tt.add), map[string]string{"in.json": tt.in})
			_, err := Load(dir)
			checkProblems(t, err, tt.want)
		})
	}
}

func TestComponentReadsOnlyWhatItsInputHas(t *testing.T) {
	files := map[string]string{}
	for name, content := range summarizeFiles {
		files[name] = content
	}
	files[ComponentFile] = `name: x
input: {ticket: object, audience: string}
ai:
  model: anthropic/m
  prompt: prompt.md
  input: "${input.ticket.subject} for ${input.audience.name}"
  output: schema.json
tests:
  - input: {ticket: {subject: Down}, audience: all}
    expect:
      output.summary != "": true
      output.sumary: x
      input.audience.name: x
`
	_, err := LoadComponent(writeFiles(t, t.TempDir(), files))
	checkProblems(t, err, []string{
		`3: component "x": input.audience is a single value, so there is no input.audience.name`,
		`12: test 1: expect: output has no sumary; it has summary`,
		`13: test 1: expect: input.audience is a single value, so there is no input.audience.name`,
	})
}

func TestSchemaShape(t *testing.T) {
	// What is not spelled out plainly is taken to allow anything.
	for _, schema := range []string{
		`{}`, `{"type": "array", "items": {"type": "string"}}`, `{"type": "object"}`,
		`{"type": "object", "properties": {"a": {"type": "string"}}, "allOf": [{"required": ["b"]}]}`,
		`{"$ref": "#/defs/x"}`, `{"type": ["string", "null"]}`,
	} {
		if s := schemaShape(mustDecode(t, schema), true); s != nil {
			t.Errorf("schemaShape(%s) = %+v, want nothing known", schema, s)
		}
	}
	nested := schemaShape(mustDecode(t, `{"type": "object", "properties": {
		"who": {"type": "object", "properties": {"name": {"type": "string"}}, "additionalProperties": true},
		"n": {"type": "integer"}, "tags": {"type": "array"}}}`), true)
	for path, want := range map[string]string{
		"who.name":       "",
		"who.other.deep": "",
		"tags.anything":  "",
		"n.x":            "v.n is a single value, so there is no v.n.x",
		"x":              "v has no x; it has n, tags, who",
	} {
		if got := nested.misread("v", strings.Split(path, ".")); got != want {
			t.Errorf("reading %s: %q, want %q", path, got, want)
		}
	}
}

func mustDecode(t *testing.T, text string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

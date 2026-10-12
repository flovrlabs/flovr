package yamledit

import (
	"os"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const doc = `name: triage-ticket

trigger:
  webhook: /tickets

steps:
  # A decision model answers typed questions.
  - id: classify
    decide:
      model: openrouter/cloudflare/clef  # fast and cheap
      input: ${trigger.body}
      questions:
        category:
          ask: What does the customer need?
          choice:
            refund: They want money back for a charge.
            other: Anything else.
        priority:
          ask: "How urgent is this ticket?"
          score:
            - low: No time pressure.
            - high: Money is at risk right now.

  # Only urgent tickets need a summary.
  - id: summarize
    # Skip everything that is not urgent.
    when: classify.priority.value == "high"
    ai:
      model: openrouter/deepseek/deepseek-v4.1-flash
      prompt: prompts/summarize.md
`

// changed returns the lines of after that are not in before, and the lines of
// before that are not in after.
func changed(before, after string) (added, removed []string) {
	count := map[string]int{}
	for _, l := range strings.Split(before, "\n") {
		count[l]++
	}
	for _, l := range strings.Split(after, "\n") {
		if count[l] > 0 {
			count[l]--
		} else {
			added = append(added, l)
		}
	}
	for _, l := range strings.Split(before, "\n") {
		if count[l] > 0 {
			count[l]--
			removed = append(removed, l)
		}
	}
	return added, removed
}

func TestApplyChangesOnlyTheLinesOfTheValue(t *testing.T) {
	tests := []struct {
		name    string
		edit    Edit
		added   []string
		removed []string
	}{
		{
			name:    "a plain value",
			edit:    Edit{Path: []any{"steps", 1, "ai", "model"}, Value: "anthropic/claude-haiku-5-5"},
			added:   []string{"      model: anthropic/claude-haiku-5-5"},
			removed: []string{"      model: openrouter/deepseek/deepseek-v4.1-flash"},
		},
		{
			name:    "a value followed by a comment keeps the comment",
			edit:    Edit{Path: []any{"steps", 0, "decide", "model"}, Value: "openrouter/other/model"},
			added:   []string{"      model: openrouter/other/model  # fast and cheap"},
			removed: []string{"      model: openrouter/cloudflare/clef  # fast and cheap"},
		},
		{
			name:    "a quoted value keeps its quotes",
			edit:    Edit{Path: []any{"steps", 0, "decide", "questions", "priority", "ask"}, Value: "How bad is it?"},
			added:   []string{`          ask: "How bad is it?"`},
			removed: []string{`          ask: "How urgent is this ticket?"`},
		},
		{
			name:    "a value that YAML would misread is quoted",
			edit:    Edit{Path: []any{"steps", 1, "when"}, Value: `!(classify.category.value in ["refund"])`},
			added:   []string{`    when: '!(classify.category.value in ["refund"])'`},
			removed: []string{`    when: classify.priority.value == "high"`},
		},
		{
			name:    "a value inside a list item",
			edit:    Edit{Path: []any{"steps", 0, "decide", "questions", "priority", "score", 1, "high"}, Value: "The product is down."},
			added:   []string{"            - high: The product is down."},
			removed: []string{"            - high: Money is at risk right now."},
		},
		{
			name:    "a top-level value",
			edit:    Edit{Path: []any{"name"}, Value: "triage"},
			added:   []string{"name: triage"},
			removed: []string{"name: triage-ticket"},
		},
		{
			name:  "a new key behind another",
			edit:  Edit{Path: []any{"steps", 0, "when"}, Value: "trigger.body != null", After: "id"},
			added: []string{"    when: trigger.body != null"},
		},
		{
			name:  "a new key goes last by default",
			edit:  Edit{Path: []any{"steps", 1, "ai", "output"}, Value: "schemas/summary.json"},
			added: []string{"      output: schemas/summary.json"},
		},
		{
			name: "a removed key takes the comment above it along",
			edit: Edit{Path: []any{"steps", 1, "when"}, Remove: true},
			removed: []string{
				"    # Skip everything that is not urgent.",
				`    when: classify.priority.value == "high"`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Apply([]byte(doc), tt.edit)
			if err != nil {
				t.Fatal(err)
			}
			added, removed := changed(doc, string(out))
			if strings.Join(added, "\n") != strings.Join(tt.added, "\n") {
				t.Errorf("added lines:\n%s\nwant:\n%s", strings.Join(added, "\n"), strings.Join(tt.added, "\n"))
			}
			if strings.Join(removed, "\n") != strings.Join(tt.removed, "\n") {
				t.Errorf("removed lines:\n%s\nwant:\n%s", strings.Join(removed, "\n"), strings.Join(tt.removed, "\n"))
			}
		})
	}
}

func TestApplyWritesTextOfSeveralLinesAsABlock(t *testing.T) {
	out, err := Apply([]byte(doc), Edit{
		Path:  []any{"steps", 0, "decide", "questions", "category", "ask"},
		Value: "What does the customer need?\n\nPick one.",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "          ask: |-\n            What does the customer need?\n\n            Pick one.\n          choice:\n"
	if !strings.Contains(string(out), want) {
		t.Errorf("result does not contain %q:\n%s", want, out)
	}
}

func TestApplyPutsANewKeyWhereAsked(t *testing.T) {
	out, err := Apply([]byte(doc), Edit{Path: []any{"steps", 0, "when"}, Value: "true", After: "id"})
	if err != nil {
		t.Fatal(err)
	}
	// "true" is text here, so it must not be written as the boolean.
	want := "  - id: classify\n    when: \"true\"\n    decide:\n"
	if !strings.Contains(string(out), want) {
		t.Errorf("result does not contain %q:\n%s", want, out)
	}
}

func TestApplyWritesTextThatNeedsQuotes(t *testing.T) {
	for _, value := range []string{
		"", "true", "null", "123", "1.5", "2026-10-12", "a: b", "x #y", " padded ", "- item", "{curly}",
		"it's", `say "hi"`, "tab\there", "ends with newline\n", "two\nlines\n\n", " starts with space\nthen more",
		strings.Repeat("a long line of words ", 30), "ภาษาไทย", "~", "*star", "&and", "@at", "`tick`", "%percent",
	} {
		out, err := Apply([]byte(doc), Edit{Path: []any{"steps", 1, "ai", "prompt"}, Value: value})
		if err != nil {
			t.Errorf("%q: %v", value, err)
			continue
		}
		// Apply reads its result back, so success means the value survived.
		// A value of one line must also stay on one line.
		if !strings.Contains(value, "\n") {
			if added, removed := changed(doc, string(out)); len(added) != 1 || len(removed) != 1 {
				t.Errorf("%q: changed %d lines to %d, want one to one:\n%s", value, len(removed), len(added), strings.Join(added, "\n"))
			}
		}
	}
}

func TestApplyBackAndForthRestoresTheFile(t *testing.T) {
	path := []any{"steps", 0, "decide", "questions", "category", "ask"}
	out, err := Apply([]byte(doc), Edit{Path: path, Value: "One\nTwo\n"})
	if err != nil {
		t.Fatal(err)
	}
	out, err = Apply(out, Edit{Path: path, Value: "What does the customer need?"})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != doc {
		t.Errorf("the file was not restored:\n%s", out)
	}

	out, err = Apply([]byte(doc), Edit{Path: []any{"steps", 0, "when"}, Value: "x", After: "id"})
	if err != nil {
		t.Fatal(err)
	}
	out, err = Apply(out, Edit{Path: []any{"steps", 0, "when"}, Remove: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != doc {
		t.Errorf("the file was not restored:\n%s", out)
	}
}

func TestApplyOtherValues(t *testing.T) {
	out, err := Apply([]byte("a: 1\nb: yes please\n"),
		Edit{Path: []any{"a"}, Value: 2},
		Edit{Path: []any{"b"}, Value: true},
		Edit{Path: []any{"c"}, Value: 0.5},
		Edit{Path: []any{"d"}, Value: nil},
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := "a: 2\nb: true\nc: 0.5\nd: null\n"; string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestApplyKeepsLineEndings(t *testing.T) {
	out, err := Apply([]byte("a: 1\r\nb: 2\r\n"), Edit{Path: []any{"a"}, Value: "x"}, Edit{Path: []any{"c"}, Value: "y"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "a: x\r\nb: 2\r\nc: y\r\n"; string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}

	// A file that does not end with a line break still does not.
	out, err = Apply([]byte("a: 1\nb: 2"), Edit{Path: []any{"c"}, Value: "y"}, Edit{Path: []any{"b"}, Remove: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := "a: 1\nc: y"; string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestApplyReplacesAValueOfSeveralLines(t *testing.T) {
	src := "a:\n  - one\n  - two\n\n# about b\nb: |\n  first\n  second\n\nc:\n- x\n- y\nd: 4\n"
	out, err := Apply([]byte(src), Edit{Path: []any{"b"}, Value: "short"}, Edit{Path: []any{"c"}, Remove: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := "a:\n  - one\n  - two\n\n# about b\nb: short\n\nd: 4\n"; string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestApplyRefusesWhatItCannotDoInPlace(t *testing.T) {
	tests := []struct {
		name string
		src  string
		edit Edit
		want string
	}{
		{"an inline mapping", "a: {b: 1, c: 2}\n", Edit{Path: []any{"a", "b"}, Value: "x"}, "written inline"},
		{"a missing parent", doc, Edit{Path: []any{"steps", 5, "when"}, Value: "x"}, "there is no steps[5]"},
		{"a list item", doc, Edit{Path: []any{"steps", 0}, Value: "x"}, "not a list item"},
		{"a value that is not a mapping", doc, Edit{Path: []any{"name", "x"}, Value: "x"}, "not a mapping"},
		{"the first key of a list item", doc, Edit{Path: []any{"steps", 0, "id"}, Remove: true}, "first key of a list item"},
		{"the only key", doc, Edit{Path: []any{"trigger", "webhook"}, Remove: true}, "only key"},
		{"a list as value", doc, Edit{Path: []any{"name"}, Value: []string{"a"}}, "cannot be written"},
		{"a comment in the value", "a:\n  # explains b\n  b: 1\nc: 2\n", Edit{Path: []any{"a"}, Value: "x"}, "in place"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Apply([]byte(tt.src), tt.edit)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want one containing %q; output:\n%s", err, tt.want, out)
			}
		})
	}
}

func TestApplyRemovingAMissingKeyChangesNothing(t *testing.T) {
	out, err := Apply([]byte(doc), Edit{Path: []any{"steps", 0, "when"}, Remove: true})
	if err != nil || string(out) != doc {
		t.Errorf("err = %v, changed = %v", err, string(out) != doc)
	}
}

// The example workflow is the file people will edit first.
func TestApplyOnTheExample(t *testing.T) {
	src, err := os.ReadFile("../../examples/triage-ticket/workflow.yaml")
	if err != nil {
		t.Fatal(err)
	}
	out, err := Apply(src,
		Edit{Path: []any{"steps", 0, "decide", "questions", "category", "choice", "refund"}, Value: "They want their money back."},
		Edit{Path: []any{"steps", 2, "with", "summary"}, Value: "${summarize.summary} (urgent)"},
		Edit{Path: []any{"steps", 3, "when"}, Value: `classify.priority.value == "high"`},
		Edit{Path: []any{"trigger", "webhook"}, Value: "/support/tickets"},
	)
	if err != nil {
		t.Fatal(err)
	}
	added, removed := changed(string(src), string(out))
	want := []string{
		"  webhook: /support/tickets",
		"            refund: They want their money back.",
		"      summary: ${summarize.summary} (urgent)",
		`    when: classify.priority.value == "high"`,
	}
	if strings.Join(added, "\n") != strings.Join(want, "\n") || len(removed) != 4 {
		t.Errorf("added:\n%s\nremoved:\n%s", strings.Join(added, "\n"), strings.Join(removed, "\n"))
	}
}

// Whatever value of the example is changed, nothing else in the file moves.
func TestApplyOnEveryValueOfTheExample(t *testing.T) {
	src, err := os.ReadFile("../../examples/triage-ticket/workflow.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		t.Fatal(err)
	}
	var paths [][]any
	var walk func(n *yaml.Node, path []any)
	walk = func(n *yaml.Node, path []any) {
		switch n.Kind {
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				p := append(slices.Clone(path), n.Content[i].Value)
				if n.Content[i+1].Kind == yaml.ScalarNode {
					paths = append(paths, p)
				}
				walk(n.Content[i+1], p)
			}
		case yaml.SequenceNode:
			for i, item := range n.Content {
				walk(item, append(slices.Clone(path), i))
			}
		}
	}
	walk(doc.Content[0], nil)
	if len(paths) < 50 {
		t.Fatalf("found only %d values in the example", len(paths))
	}

	for _, path := range paths {
		for value, lines := range map[string]int{"changed": 1, `a "quoted" one: with # marks`: 1, "two\nlines": 3} {
			out, err := Apply(src, Edit{Path: path, Value: value})
			if err != nil {
				t.Errorf("%s = %q: %v", pathString(path), value, err)
				continue
			}
			if added, removed := changed(string(src), string(out)); len(added) != lines || len(removed) != 1 {
				t.Errorf("%s = %q: replaced %d lines with %d, want 1 with %d:\n%s", pathString(path), value, len(removed), len(added), lines, strings.Join(added, "\n"))
			}
		}
	}
}

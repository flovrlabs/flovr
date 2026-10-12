package server

import (
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"aia/internal/workflow"
)

// describer describes the values of one file, a workflow's or a component's,
// offering for change those that the way the file is written allows.
type describer struct {
	writable func(path ...any) bool
}

// offer lists a value as one the page may change, unless the way it is
// written in the file rules that out.
func (d describer) offer(to *[]fieldView, f fieldView) {
	if !d.writable(f.Path...) {
		return
	}
	if f.Kind == "" {
		f.Kind = kindText
	}
	*to = append(*to, f)
}

// texts offers every piece of text in v, however deep it is.
func (d describer) texts(to *[]fieldView, group, label string, path []any, v any) {
	switch v := v.(type) {
	case string:
		if label == "" {
			label = group
		}
		d.offer(to, fieldView{Label: label, Group: group, Path: path, Value: v})
	case map[string]any:
		for _, key := range slices.Sorted(maps.Keys(v)) {
			name := key
			if label != "" {
				name = label + "." + key
			}
			d.texts(to, group, name, append(slices.Clone(path), key), v[key])
		}
	}
}

// step describes the step written at base in the file. reads is what it
// refers to and outputs what later steps can read from it. A component that
// does one kind of work is described as a step written at the top of its
// file, which has no condition.
func (d describer) step(s workflow.Step, base []any, reads, outputs []string) stepView {
	step := stepView{ID: s.ID, Kind: s.Kind(), When: s.When, Reads: reads, Fields: [][2]string{}, Outputs: outputs}
	if step.Reads == nil {
		step.Reads = []string{}
	}
	add := func(label, value string) {
		if value != "" {
			step.Fields = append(step.Fields, [2]string{label, value})
		}
	}
	at := func(path ...any) []any { return append(slices.Clone(base), path...) }
	edit := func(f fieldView) { d.offer(&step.Edit, f) }
	texts := func(group, label string, path []any, v any) { d.texts(&step.Edit, group, label, path, v) }

	if len(base) > 0 {
		edit(fieldView{
			Label: "Run only when", Path: at("when"), Value: s.When, Kind: kindExpression, Optional: true,
			Hint: "Empty means the step always runs.",
		})
	}
	if c := s.Component(); c != nil {
		// What the component does is shown but is not this file's to
		// change; the input the step gives it is.
		step.Component, step.Uses, step.Subtitle = c.Name, s.Use, c.Name
		step.Target = componentID(c)
		add("component", s.Use)
		switch {
		case c.AI != nil:
			add("model", c.AI.Model)
		case c.Decide != nil:
			add("model", c.Decide.Model)
			step.Questions = questions(c.Decide)
		case c.HTTP != nil:
			add("request", strings.ToUpper(c.HTTP.Method)+" "+c.HTTP.URL)
		case len(c.Steps) > 0:
			for _, in := range c.Steps {
				step.Inner = append(step.Inner, innerView{ID: in.ID, Kind: in.Kind(), Subtitle: subtitle(in), When: in.When})
			}
			if out, ok := c.Output.(map[string]any); ok {
				add("gives back", strings.Join(slices.Sorted(maps.Keys(out)), ", "))
			}
		}
		for _, p := range c.Input {
			texts("Input", p.Name, at("with", p.Name), s.With[p.Name])
		}
	}
	switch {
	case s.AI != nil:
		step.Subtitle = shortModel(s.AI.Model)
		add("model", s.AI.Model)
		add("prompt", s.AI.Prompt)
		add("output", s.AI.Output)
		edit(fieldView{Label: "Model", Path: at("ai", "model"), Value: s.AI.Model})
		edit(fieldView{Label: "Prompt", Path: at("ai", "prompt"), Value: s.AI.Prompt, Kind: kindPrompt})
		texts("", "Input", at("ai", "input"), s.AI.Input)
		edit(fieldView{Label: "Output schema", Path: at("ai", "output"), Value: s.AI.Output})
	case s.Decide != nil:
		step.Subtitle = shortModel(s.Decide.Model)
		add("model", s.Decide.Model)
		edit(fieldView{Label: "Model", Path: at("decide", "model"), Value: s.Decide.Model})
		texts("", "Input", at("decide", "input"), s.Decide.Input)
		step.Questions = questions(s.Decide)
		for _, name := range slices.Sorted(maps.Keys(s.Decide.Questions)) {
			q := s.Decide.Questions[name]
			spec := q.Spec()

			in := func(path ...any) []any { return append(at("decide", "questions", name), path...) }
			edit(fieldView{Label: "Question", Group: name, Path: in("ask"), Value: q.Ask})
			list := "choice"
			if q.Score != nil {
				list = "score"
			}
			for j, o := range spec.Options {
				// An answer's meaning is the value of its name, in a
				// mapping of answers or in an item of a list of them.
				edit(fieldView{Label: o.Name, Group: name, Path: in(list, o.Name), Value: o.Meaning})
				edit(fieldView{Label: o.Name, Group: name, Path: in(list, j, o.Name), Value: o.Meaning})
			}
			if q.YesIf != "" {
				edit(fieldView{Label: "Yes if", Group: name, Path: in("yes_if"), Value: q.YesIf})
				edit(fieldView{Label: "No if", Group: name, Path: in("no_if"), Value: q.NoIf})
			}
		}
	case s.HTTP != nil:
		// The URL is shown as written, with its ${...} unresolved, so no
		// secret it resolves to leaves the server.
		step.Subtitle = strings.ToUpper(s.HTTP.Method) + " " + s.HTTP.URL
		add("request", step.Subtitle)
		if body, ok := s.HTTP.Body.(map[string]any); ok {
			add("sends", strings.Join(slices.Sorted(maps.Keys(body)), ", "))
		}
		edit(fieldView{
			Label: "Method", Path: at("http", "method"), Value: strings.ToUpper(s.HTTP.Method),
			Kind: kindChoice, Options: workflow.Methods,
		})
		edit(fieldView{Label: "URL", Path: at("http", "url"), Value: s.HTTP.URL})
		for _, name := range slices.Sorted(maps.Keys(s.HTTP.Headers)) {
			edit(fieldView{Label: name, Group: "Headers", Path: at("http", "headers", name), Value: s.HTTP.Headers[name]})
		}
		texts("Body", "", at("http", "body"), s.HTTP.Body)
	}
	return step
}

func describe(w *workflow.Workflow) workflowView {
	view := workflowView{Name: w.Name, Path: w.Path, Tests: len(w.Tests), Steps: []stepView{}}
	d := describer{w.Writable}

	if len(w.Tests) > 0 {
		if event, err := w.Fixture(w.Tests[0].Fixture); err == nil {
			if fields, ok := event.(map[string]any); ok {
				view.Sample = fields["body"]
			}
		}
	}
	if r := w.Respond; r != nil {
		rv := &respondView{Status: r.Status, Body: r.Body, Reads: w.RespondReads()}
		status := ""
		if r.Status != 0 {
			status = strconv.Itoa(r.Status)
		} else {
			rv.Status = http.StatusOK
		}
		d.offer(&rv.Edit, fieldView{
			Label: "Status", Path: []any{"respond", "status"}, Value: status,
			Kind: kindNumber, Optional: true, Hint: "Empty means 200.",
		})
		for _, name := range slices.Sorted(maps.Keys(r.Headers)) {
			d.offer(&rv.Edit, fieldView{Label: name, Group: "Headers", Path: []any{"respond", "headers", name}, Value: r.Headers[name]})
		}
		d.texts(&rv.Edit, "Body", "", []any{"respond", "body"}, r.Body)
		view.Respond = rv
	}
	switch t := w.Trigger; {
	case t != nil && t.Webhook != "":
		view.Trigger, view.Input = "POST "+t.Webhook, t.Input
		d.offer(&view.Edit, fieldView{Label: "Webhook path", Path: []any{"trigger", "webhook"}, Value: t.Webhook})
		d.offer(&view.Edit, fieldView{
			Label: "Body schema", Path: []any{"trigger", "input"}, Value: t.Input, Optional: true,
			Hint: "A JSON Schema file. A request whose body does not match it is refused. Empty takes any body.",
		})
	case t != nil && t.Cron != "":
		view.Trigger = "cron " + t.Cron
		d.offer(&view.Edit, fieldView{Label: "Schedule", Path: []any{"trigger", "cron"}, Value: t.Cron})
	}
	for i, s := range w.Steps {
		view.Steps = append(view.Steps, d.step(s, []any{"steps", i}, w.Reads(i), w.Outputs(s.ID)))
	}
	return view
}

// componentID is the name the page knows a component by: the path of its
// folder, which no two components share.
func componentID(c *workflow.Component) string {
	return filepath.ToSlash(filepath.Dir(c.Path))
}

// describeComponent describes a component in the form of a workflow, which
// is how the page shows it.
func describeComponent(c *workflow.Component) workflowView {
	view := workflowView{
		Name: componentID(c), Title: c.Name, Path: c.Path, Tests: len(c.Tests), Steps: []stepView{},
		Component: true, Description: c.Description, Inputs: []inputView{}, Fixed: true,
	}
	for _, p := range c.Input {
		view.Inputs = append(view.Inputs, inputView{p.Name, p.Type})
	}
	d := describer{c.Writable}
	if len(c.Steps) == 0 {
		// Its one kind of work stands as a step of its own name.
		work := workflow.Step{ID: c.Name, AI: c.AI, Decide: c.Decide, HTTP: c.HTTP}
		view.Steps = append(view.Steps, d.step(work, nil, []string{"input"}, c.Gives()))
		return view
	}
	for i, s := range c.Steps {
		view.Steps = append(view.Steps, d.step(s, []any{"steps", i}, c.Reads(i), c.Outputs(s.ID)))
	}
	gives := &givesView{Body: c.Output, Fields: c.Gives(), Reads: c.OutputReads()}
	if gives.Fields == nil {
		gives.Fields = []string{}
	}
	d.texts(&gives.Edit, "Output", "", []any{"output"}, c.Output)
	view.Gives = gives
	return view
}

// componentCases describes the tests of a component.
func componentCases(c *workflow.Component) []caseView {
	var views []caseView
	for i, t := range c.Tests {
		view := caseView{Name: t.Name, Input: t.Input, Expect: []expectView{}}
		if view.Name == "" {
			view.Name = "test " + strconv.Itoa(i+1)
		}
		for _, key := range slices.Sorted(maps.Keys(t.Expect)) {
			view.Expect = append(view.Expect, expectView{key, t.Expect[key]})
		}
		views = append(views, view)
	}
	return views
}

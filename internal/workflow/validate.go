package workflow

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/robfig/cron/v3"
	"go.yaml.in/yaml/v3"

	"aia/internal/ai"
	"aia/internal/expr"
)

var idRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Methods are the request methods an http step can use.
var Methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE"}

// checker collects the problems of one workflow or component file.
type checker struct {
	path  string     // the file; paths written in it are relative to its directory
	root  *yaml.Node // the parsed file, for line numbers
	w     *Workflow  // nil for a component file
	items []Problem

	components map[string]*Component // those loaded so far, by path
	shapes     map[string]*shape     // what is known of the values expressions read, by name
	loading    []string              // the component files being loaded, the one that uses the next before it
}

func (c *checker) addf(line int, format string, args ...any) {
	c.items = append(c.items, Problem{line, fmt.Sprintf(format, args...)})
}

func (c *checker) line(path ...any) int { return lineOf(c.root, path...) }

// under returns the line of the value at path below the value at base.
func (c *checker) under(base []any, path ...any) int {
	return c.line(append(slices.Clone(base), path...)...)
}

func (c *checker) run() {
	w := c.w
	if w.Name == "" {
		c.addf(c.line("name"), "name is required")
	}
	c.trigger()
	if len(w.Steps) == 0 {
		c.addf(c.line("steps"), "at least one step is required")
	}

	// What a request body must match says what steps can read from it.
	c.shapes = map[string]*shape{}
	if t := w.Trigger; t != nil && t.Input != "" {
		if _, doc, err := loadSchema(c.path, t.Input); err == nil {
			c.shapes["trigger"] = &shape{open: true, fields: map[string]*shape{"body": schemaShape(doc, false)}}
		}
	}

	ids := c.steps(w.Steps, "trigger")
	c.respond(ids)
	c.tests(ids)
}

// reserved are the names expressions use for things other than steps.
var reserved = []string{"trigger", "env", "response", "input", "output"}

// steps checks the steps of a workflow or of a component made of steps, and
// returns the ids that expressions after them may refer to. given is what
// expressions see besides steps: "trigger" in a workflow, "input" in a
// component.
func (c *checker) steps(steps []Step, given string) []string {
	// ids holds the steps a later step may refer to.
	var ids []string
	for i, s := range steps {
		line := c.line("steps", i)
		label := fmt.Sprintf("step %q", s.ID)
		usable := false
		switch {
		case s.ID == "":
			label = fmt.Sprintf("step %d", i+1)
			c.addf(line, "%s: id is required", label)
		case !idRE.MatchString(s.ID):
			c.addf(line, "%s: id may contain only letters, digits and underscores, and must not start with a digit", label)
		case slices.Contains(reserved, s.ID):
			c.addf(line, "%s: this id is reserved", label)
		case slices.Contains(ids, s.ID):
			c.addf(line, "%s: this id is already used by an earlier step", label)
		default:
			usable = true
		}

		env, err := expr.Vars(append([]string{given}, ids...)...)
		if err != nil {
			c.addf(line, "%s: %v", label, err)
			continue
		}
		if s.When != "" {
			if err := env.CheckCondition(s.When); err != nil {
				c.addf(c.line("steps", i, "when"), "%s: when: %v", label, err)
			}
		}
		kinds := 0
		for _, present := range []bool{s.AI != nil, s.Decide != nil, s.HTTP != nil, s.Use != ""} {
			if present {
				kinds++
			}
		}
		base := []any{"steps", i}
		switch {
		case kinds > 1:
			c.addf(line, "%s: a step can have only one of ai, decide, http, use", label)
		case s.AI != nil:
			c.ai(base, label, s.AI, env)
		case s.Decide != nil:
			c.decide(base, label, s.Decide, env)
		case s.HTTP != nil:
			c.http(base, label, s.HTTP, env)
		case s.Use != "":
			steps[i].component = c.use(base, label, s, env)
		default:
			c.addf(line, "%s: needs one of ai, decide, http, use", label)
		}
		if s.With != nil && s.Use == "" {
			c.addf(c.under(base, "with"), "%s: with is only for a step that has use", label)
		}

		c.reads(line, label, s.expressions(), c.shapes)
		if usable {
			ids = append(ids, s.ID)
			if comp := steps[i].component; comp != nil {
				c.shapes[s.ID] = comp.shape()
			} else {
				c.shapes[s.ID] = outputShape(c.path, s.AI, s.Decide, s.HTTP)
			}
		}
	}
	return ids
}

// respond checks what the workflow answers its caller with, which may refer
// to every step.
func (c *checker) respond(ids []string) {
	r := c.w.Respond
	if r == nil {
		return
	}
	if t := c.w.Trigger; t == nil || t.Webhook == "" {
		c.addf(c.line("respond"), "respond: only a workflow started by a webhook has a caller to answer")
	}
	if r.Status != 0 && (r.Status < 200 || r.Status > 599) {
		c.addf(c.line("respond", "status"), "respond: status must be an HTTP status from 200 to 599")
	}
	env, err := expr.NewEnv(ids)
	if err != nil {
		c.addf(c.line("respond"), "respond: %v", err)
		return
	}
	for _, name := range slices.Sorted(maps.Keys(r.Headers)) {
		if err := env.Check(r.Headers[name]); err != nil {
			c.addf(c.line("respond", "headers", name), "respond: headers.%s: %v", name, err)
		}
	}
	if err := env.Check(r.Body); err != nil {
		c.addf(c.line("respond", "body"), "respond: body: %v", err)
	}
	c.reads(c.line("respond"), "respond", r.expressions(), c.shapes)
	c.shapes["response"] = responseShape(r)
}

func (c *checker) trigger() {
	t := c.w.Trigger
	if t == nil {
		return
	}
	set := 0
	if t.Webhook != "" {
		set++
		if !strings.HasPrefix(t.Webhook, "/") {
			c.addf(c.line("trigger", "webhook"), "trigger: webhook must be a path starting with /")
		}
	}
	if t.Cron != "" {
		set++
		if _, err := cron.ParseStandard(t.Cron); err != nil {
			c.addf(c.line("trigger", "cron"), "trigger: cron %q is not a schedule of five fields (minute hour day month weekday)", t.Cron)
		}
	}
	if t.Manual {
		set++
	}
	if set != 1 {
		c.addf(c.line("trigger"), "trigger: needs exactly one of webhook, cron, manual")
	}
	if t.Input != "" {
		at := c.line("trigger", "input")
		if t.Webhook == "" {
			c.addf(at, "trigger: input describes the body of a webhook request, so it needs webhook")
		}
		if _, _, err := loadSchema(c.path, t.Input); errors.Is(err, fs.ErrNotExist) {
			c.addf(at, "trigger: schema file not found: %s", t.Input)
		} else if err != nil {
			c.addf(at, "trigger: %s is not a valid JSON Schema: %v", t.Input, err)
		}
	}
}

// ai checks the ai part of the step or component at base.
func (c *checker) ai(base []any, label string, s *AIStep, env *expr.Env) {
	at := func(field string) int { return c.under(base, "ai", field) }

	if s.Model == "" {
		c.addf(at("model"), "%s: ai.model is required", label)
	} else if _, _, err := ai.SplitModel(s.Model); err != nil {
		c.addf(at("model"), "%s: ai.model %v", label, err)
	}

	if s.Prompt == "" {
		c.addf(at("prompt"), "%s: ai.prompt is required", label)
	} else if info, err := os.Stat(resolve(c.path, s.Prompt)); err != nil || info.IsDir() {
		c.addf(at("prompt"), "%s: prompt file not found: %s", label, s.Prompt)
	}

	if s.Input == nil {
		c.addf(at("input"), "%s: ai.input is required", label)
	} else if err := env.Check(s.Input); err != nil {
		c.addf(at("input"), "%s: ai.input: %v", label, err)
	}

	if s.Output == "" {
		c.addf(at("output"), "%s: ai.output is required", label)
	} else if _, _, err := loadSchema(c.path, s.Output); errors.Is(err, fs.ErrNotExist) {
		c.addf(at("output"), "%s: schema file not found: %s", label, s.Output)
	} else if err != nil {
		c.addf(at("output"), "%s: %s is not a valid JSON Schema: %v", label, s.Output, err)
	}
}

func (c *checker) decide(base []any, label string, s *DecideStep, env *expr.Env) {
	at := func(path ...any) int {
		return c.under(base, append([]any{"decide"}, path...)...)
	}

	if s.Model == "" {
		c.addf(at("model"), "%s: decide.model is required", label)
	} else if provider, _, err := ai.SplitModel(s.Model); err != nil {
		c.addf(at("model"), "%s: decide.model %v", label, err)
	} else if !slices.Contains(ai.DecideProviders, provider) {
		c.addf(at("model"), "%s: decide.model must be a decision model, which come from %s",
			label, strings.Join(ai.DecideProviders, ", "))
	}

	if s.Input == nil {
		c.addf(at("input"), "%s: decide.input is required", label)
	} else if err := env.Check(s.Input); err != nil {
		c.addf(at("input"), "%s: decide.input: %v", label, err)
	}

	if len(s.Questions) == 0 {
		c.addf(at("questions"), "%s: decide.questions needs at least one question", label)
	}
	for _, name := range slices.Sorted(maps.Keys(s.Questions)) {
		q := s.Questions[name]
		line := at("questions", name)
		label := fmt.Sprintf("%s: question %q", label, name)

		if !idRE.MatchString(name) {
			c.addf(line, "%s: its name may contain only letters, digits and underscores, and must not start with a digit", label)
		}
		if q.Ask == "" {
			c.addf(at("questions", name, "ask"), "%s: ask is required", label)
		}
		if q.Choice != nil && q.Score != nil {
			c.addf(line, "%s: can have only one of choice, score", label)
		}
		c.options(at("questions", name, "choice"), label+": choice", q.Choice)
		c.options(at("questions", name, "score"), label+": score", q.Score)

		yesNo := q.Choice == nil && q.Score == nil
		switch {
		case !yesNo && (q.YesIf != "" || q.NoIf != ""):
			c.addf(line, "%s: yes_if and no_if are only for yes/no questions, which have no choice or score", label)
		case (q.YesIf == "") != (q.NoIf == ""):
			c.addf(line, "%s: yes_if and no_if must be given together", label)
		}
	}
}

// options checks the answers of a choice or score question. A nil opts means
// the question has no such key.
func (c *checker) options(line int, label string, opts []ai.Option) {
	if opts == nil {
		return
	}
	if len(opts) < 2 {
		c.addf(line, "%s needs at least two answers", label)
	}
	seen := map[string]bool{}
	for _, o := range opts {
		switch {
		case o.Name == "":
			c.addf(line, "%s has an answer without a name", label)
		case seen[o.Name]:
			c.addf(line, "%s lists %q twice", label, o.Name)
		}
		seen[o.Name] = true
	}
}

func (c *checker) http(base []any, label string, s *HTTPStep, env *expr.Env) {
	at := func(field string) int { return c.under(base, "http", field) }

	if s.Method == "" {
		c.addf(at("method"), "%s: http.method is required", label)
	} else if !slices.Contains(Methods, strings.ToUpper(s.Method)) {
		c.addf(at("method"), "%s: http.method must be one of %s", label, strings.Join(Methods, ", "))
	}

	if s.URL == "" {
		c.addf(at("url"), "%s: http.url is required", label)
	} else if err := env.Check(s.URL); err != nil {
		c.addf(at("url"), "%s: http.url: %v", label, err)
	}

	for name, value := range s.Headers {
		if err := env.Check(value); err != nil {
			c.addf(c.under(base, "http", "headers", name), "%s: http.headers.%s: %v", label, name, err)
		}
	}
	if err := env.Check(s.Body); err != nil {
		c.addf(at("body"), "%s: http.body: %v", label, err)
	}
}

func (c *checker) tests(ids []string) {
	// A test of a workflow that answers its caller can expect things of the
	// answer, as response.status and response.body.
	names := append([]string{"trigger"}, ids...)
	if c.w.Respond != nil {
		names = append(names, "response")
	}
	env, err := expr.Vars(names...)
	if err != nil {
		c.addf(c.line("tests"), "tests: %v", err)
		return
	}
	for i, t := range c.w.Tests {
		label := fmt.Sprintf("test %q", t.Name)
		if t.Name == "" {
			label = fmt.Sprintf("test %d", i+1)
		}

		at := c.line("tests", i, "fixture")
		if t.Fixture == "" {
			c.addf(at, "%s: fixture is required", label)
		} else if event, err := loadJSON(c.path, t.Fixture); errors.Is(err, fs.ErrNotExist) {
			c.addf(at, "%s: fixture file not found: %s", label, t.Fixture)
		} else if err != nil {
			c.addf(at, "%s: fixture %s is not valid JSON", label, t.Fixture)
		} else if err := c.w.CheckTrigger(event); err != nil {
			// A schema that could not be loaded is reported with the trigger.
			var input *InputError
			if errors.As(err, &input) {
				c.addf(at, "%s: fixture %s does not match trigger.input: %s", label, t.Fixture, strings.Join(input.Problems, "; "))
			}
		}

		// A key is either a step's id, expecting it to be called or skipped,
		// or an expression whose value is compared.
		for key, want := range t.Expect {
			at := c.line("tests", i, "expect", key)
			if slices.Contains(ids, key) {
				if want != Called && want != Skipped {
					c.addf(at, "%s: expect.%s must be %s or %s", label, key, Called, Skipped)
				}
			} else if err := env.CheckExpr(key); err != nil {
				c.addf(at, "%s: expect.%s: %v", label, key, err)
			} else {
				c.reads(at, label+": expect", []string{key}, c.shapes)
			}
		}
	}
}

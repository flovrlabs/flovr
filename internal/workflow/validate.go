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

	"aia/internal/ai"
	"aia/internal/expr"
)

var (
	idRE    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE"}
)

type checker struct {
	w     *Workflow
	items []Problem
}

func (c *checker) addf(line int, format string, args ...any) {
	c.items = append(c.items, Problem{line, fmt.Sprintf(format, args...)})
}

func (c *checker) run() {
	w := c.w
	if w.Name == "" {
		c.addf(w.line("name"), "name is required")
	}
	c.trigger()
	if len(w.Steps) == 0 {
		c.addf(w.line("steps"), "at least one step is required")
	}

	// ids holds the steps a later step may refer to.
	var ids []string
	for i, s := range w.Steps {
		line := w.line("steps", i)
		label := fmt.Sprintf("step %q", s.ID)
		usable := false
		switch {
		case s.ID == "":
			label = fmt.Sprintf("step %d", i+1)
			c.addf(line, "%s: id is required", label)
		case !idRE.MatchString(s.ID):
			c.addf(line, "%s: id may contain only letters, digits and underscores, and must not start with a digit", label)
		case s.ID == "trigger" || s.ID == "env":
			c.addf(line, "%s: this id is reserved", label)
		case slices.Contains(ids, s.ID):
			c.addf(line, "%s: this id is already used by an earlier step", label)
		default:
			usable = true
		}

		env, err := expr.NewEnv(ids)
		if err != nil {
			c.addf(line, "%s: %v", label, err)
			continue
		}
		if s.When != "" {
			if err := env.CheckCondition(s.When); err != nil {
				c.addf(w.line("steps", i, "when"), "%s: when: %v", label, err)
			}
		}
		kinds := 0
		for _, present := range []bool{s.AI != nil, s.Decide != nil, s.HTTP != nil} {
			if present {
				kinds++
			}
		}
		switch {
		case kinds > 1:
			c.addf(line, "%s: a step can have only one of ai, decide, http", label)
		case s.AI != nil:
			c.ai(i, label, s.AI, env)
		case s.Decide != nil:
			c.decide(i, label, s.Decide, env)
		case s.HTTP != nil:
			c.http(i, label, s.HTTP, env)
		default:
			c.addf(line, "%s: needs one of ai, decide, http", label)
		}

		if usable {
			ids = append(ids, s.ID)
		}
	}
	c.tests(ids)
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
			c.addf(c.w.line("trigger", "webhook"), "trigger: webhook must be a path starting with /")
		}
	}
	if t.Cron != "" {
		set++
		if len(strings.Fields(t.Cron)) != 5 {
			c.addf(c.w.line("trigger", "cron"), "trigger: cron must have five fields (minute hour day month weekday)")
		}
	}
	if t.Manual {
		set++
	}
	if set != 1 {
		c.addf(c.w.line("trigger"), "trigger: needs exactly one of webhook, cron, manual")
	}
}

func (c *checker) ai(i int, label string, s *AIStep, env *expr.Env) {
	at := func(field string) int { return c.w.line("steps", i, "ai", field) }

	if s.Model == "" {
		c.addf(at("model"), "%s: ai.model is required", label)
	} else if _, _, err := ai.SplitModel(s.Model); err != nil {
		c.addf(at("model"), "%s: ai.model %v", label, err)
	}

	if s.Prompt == "" {
		c.addf(at("prompt"), "%s: ai.prompt is required", label)
	} else if info, err := os.Stat(c.w.Resolve(s.Prompt)); err != nil || info.IsDir() {
		c.addf(at("prompt"), "%s: prompt file not found: %s", label, s.Prompt)
	}

	if s.Input == nil {
		c.addf(at("input"), "%s: ai.input is required", label)
	} else if err := env.Check(s.Input); err != nil {
		c.addf(at("input"), "%s: ai.input: %v", label, err)
	}

	if s.Output == "" {
		c.addf(at("output"), "%s: ai.output is required", label)
	} else if _, _, err := c.w.Schema(s.Output); errors.Is(err, fs.ErrNotExist) {
		c.addf(at("output"), "%s: schema file not found: %s", label, s.Output)
	} else if err != nil {
		c.addf(at("output"), "%s: %s is not a valid JSON Schema: %v", label, s.Output, err)
	}
}

func (c *checker) decide(i int, label string, s *DecideStep, env *expr.Env) {
	at := func(path ...any) int {
		return c.w.line(append([]any{"steps", i, "decide"}, path...)...)
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

func (c *checker) http(i int, label string, s *HTTPStep, env *expr.Env) {
	at := func(field string) int { return c.w.line("steps", i, "http", field) }

	if s.Method == "" {
		c.addf(at("method"), "%s: http.method is required", label)
	} else if !slices.Contains(methods, strings.ToUpper(s.Method)) {
		c.addf(at("method"), "%s: http.method must be one of %s", label, strings.Join(methods, ", "))
	}

	if s.URL == "" {
		c.addf(at("url"), "%s: http.url is required", label)
	} else if err := env.Check(s.URL); err != nil {
		c.addf(at("url"), "%s: http.url: %v", label, err)
	}

	for name, value := range s.Headers {
		if err := env.Check(value); err != nil {
			c.addf(c.w.line("steps", i, "http", "headers", name), "%s: http.headers.%s: %v", label, name, err)
		}
	}
	if err := env.Check(s.Body); err != nil {
		c.addf(at("body"), "%s: http.body: %v", label, err)
	}
}

func (c *checker) tests(ids []string) {
	env, err := expr.NewEnv(ids)
	if err != nil {
		c.addf(c.w.line("tests"), "tests: %v", err)
		return
	}
	for i, t := range c.w.Tests {
		label := fmt.Sprintf("test %q", t.Name)
		if t.Name == "" {
			label = fmt.Sprintf("test %d", i+1)
		}

		at := c.w.line("tests", i, "fixture")
		if t.Fixture == "" {
			c.addf(at, "%s: fixture is required", label)
		} else if _, err := c.w.Fixture(t.Fixture); errors.Is(err, fs.ErrNotExist) {
			c.addf(at, "%s: fixture file not found: %s", label, t.Fixture)
		} else if err != nil {
			c.addf(at, "%s: fixture %s is not valid JSON", label, t.Fixture)
		}

		// A key is either a step's id, expecting it to be called or skipped,
		// or an expression whose value is compared.
		for key, want := range t.Expect {
			at := c.w.line("tests", i, "expect", key)
			if slices.Contains(ids, key) {
				if want != Called && want != Skipped {
					c.addf(at, "%s: expect.%s must be %s or %s", label, key, Called, Skipped)
				}
			} else if err := env.CheckExpr(key); err != nil {
				c.addf(at, "%s: expect.%s: %v", label, key, err)
			}
		}
	}
}

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/flovrlabs/flovr/internal/ai"
	"github.com/flovrlabs/flovr/internal/expr"
	"github.com/flovrlabs/flovr/internal/workflow"
)

// TestResult is the outcome of one of a workflow's tests.
type TestResult struct {
	Name     string
	Failures []string // why the test failed; empty if it passed
	Steps    []StepResult
	Duration time.Duration

	// Checks holds every expectation of the test, met or not, in the order
	// of their keys. It is empty if the run failed before they were looked at.
	Checks []Check
	// Response is what the workflow answered, if it has respond.
	Response *Response
}

// Check is one thing a test expects and, if the run did not meet it, how.
type Check struct {
	Key     string `json:"key"`
	Want    any    `json:"want"`
	Failure string `json:"failure,omitempty"`
}

// Test runs the workflow with t's fixture as the trigger and compares the
// result with what t expects. ai steps call the model; http steps are not
// sent. Environment variables the workflow reads but that are not set get a
// placeholder value, so a test needs no configuration for requests it will
// not make.
//
// The error is for a test that could not be run at all, which says nothing
// about the workflow.
func (e *Engine) Test(ctx context.Context, w *workflow.Workflow, t workflow.Test) (TestResult, error) {
	res := TestResult{Name: t.Name}
	if res.Name == "" {
		res.Name = t.Fixture
	}
	trigger, err := w.Fixture(t.Fixture)
	if err != nil {
		return res, err
	}

	env := map[string]string{}
	maps.Copy(env, e.Env)
	for _, name := range w.EnvVars() {
		if _, ok := env[name]; !ok {
			env[name] = "env." + name
		}
	}
	sandbox := &Engine{
		Model:    e.Model,
		Env:      env,
		StubHTTP: true,
		OnStep: func(r StepResult) {
			res.Steps = append(res.Steps, r)
			if e.OnStep != nil {
				e.OnStep(r)
			}
		},
	}

	start := time.Now()
	vars, resp, err := sandbox.run(ctx, w, trigger)
	res.Duration = time.Since(start)

	var unavailable *ai.UnavailableError
	if errors.As(err, &unavailable) {
		return res, err
	}
	if err != nil {
		res.Failures = []string{err.Error()}
		return res, nil
	}
	if resp != nil {
		// Expectations see the answer as a caller would, decoded from JSON.
		var seen any
		if data, err := json.Marshal(resp); err == nil {
			json.Unmarshal(data, &seen)
		}
		vars["response"] = seen
	}
	given := []string{"trigger"}
	if resp != nil {
		given = append(given, "response")
	}
	res.Response = resp
	res.Checks, err = check(t.Expect, res.Steps, vars, given...)
	res.Failures = failures(res.Checks)
	return res, err
}

// failures lists how the checks that were not met failed.
func failures(checks []Check) []string {
	var out []string
	for _, c := range checks {
		if c.Failure != "" {
			out = append(out, c.Failure)
		}
	}
	return out
}

// TestComponent runs the component on its own with t's input and compares
// the result with what t expects, under the same conditions as Test.
func (e *Engine) TestComponent(ctx context.Context, c *workflow.Component, t workflow.ComponentTest) (TestResult, error) {
	res := TestResult{Name: t.Name}
	if res.Name == "" {
		res.Name = "test of " + c.Name
	}
	input, err := c.Values(t)
	if err != nil {
		return res, err
	}

	env := map[string]string{}
	maps.Copy(env, e.Env)
	for _, name := range c.EnvVars() {
		if _, ok := env[name]; !ok {
			env[name] = "env." + name
		}
	}
	sandbox := &Engine{Model: e.Model, Env: env, StubHTTP: true}

	start := time.Now()
	step := StepResult{ID: c.Name, Kind: c.Kind(), Status: OK}
	var vars map[string]any
	step.Output, step.Detail, step.Steps, vars, step.Err = sandbox.call(ctx, c, input)
	step.Duration = time.Since(start)
	res.Duration = step.Duration

	var unavailable *ai.UnavailableError
	if errors.As(step.Err, &unavailable) {
		return res, step.Err
	}
	if step.Err != nil {
		step.Status = Failed
		res.Steps = []StepResult{step}
		res.Failures = []string{step.Err.Error()}
		return res, nil
	}
	res.Steps = []StepResult{step}

	// Expectations see the input, the output and, of a component made of
	// steps, each step.
	vars["output"] = step.Output
	res.Checks, err = check(t.Expect, step.Steps, vars, "input", "output")
	res.Failures = failures(res.Checks)
	return res, err
}

// check holds the finished run against each expectation. given names what
// expressions can read besides the steps.
func check(expect map[string]any, steps []StepResult, vars map[string]any, given ...string) ([]Check, error) {
	status := map[string]Status{}
	names := slices.Clone(given)
	for _, s := range steps {
		status[s.ID] = s.Status
		names = append(names, s.ID)
	}
	env, err := expr.Vars(names...)
	if err != nil {
		return nil, err
	}

	var checks []Check
	for _, key := range slices.Sorted(maps.Keys(expect)) {
		c := Check{Key: key, Want: expect[key]}

		if st, isStep := status[key]; isStep {
			got := workflow.Called
			if st == Skipped {
				got = workflow.Skipped
			}
			if got != c.Want {
				c.Failure = fmt.Sprintf("%s: expected %v, but it was %s", key, c.Want, got)
			}
			checks = append(checks, c)
			continue
		}

		got, err := env.EvalExpr(key, vars)
		if err != nil {
			if id, _, _ := strings.Cut(key, "."); status[id] == Skipped {
				err = fmt.Errorf("step %q was skipped", id)
			}
			c.Failure = fmt.Sprintf("%s: %v", key, err)
		} else if g, w := jsonText(got), jsonText(c.Want); g != w {
			c.Failure = fmt.Sprintf("%s: expected %s, got %s", key, w, g)
		}
		checks = append(checks, c)
	}
	return checks, nil
}

// jsonText renders v as JSON, which gives values that came from YAML and
// values that came from JSON one form to be compared in.
func jsonText(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

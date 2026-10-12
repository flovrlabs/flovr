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

	"aia/internal/ai"
	"aia/internal/expr"
	"aia/internal/workflow"
)

// TestResult is the outcome of one of a workflow's tests.
type TestResult struct {
	Name     string
	Failures []string // why the test failed; empty if it passed
	Steps    []StepResult
	Duration time.Duration
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
		OnStep:   func(r StepResult) { res.Steps = append(res.Steps, r) },
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
	res.Failures, err = unmet(t.Expect, res.Steps, vars)
	return res, err
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
	step.Output, step.Detail, step.Err = sandbox.call(ctx, c, input)
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

	results, err := expr.Vars("input", "output")
	if err != nil {
		return res, err
	}
	vars := map[string]any{"input": input, "output": step.Output, "env": env}
	for _, key := range slices.Sorted(maps.Keys(t.Expect)) {
		if failure := differs(results, key, t.Expect[key], vars); failure != "" {
			res.Failures = append(res.Failures, failure)
		}
	}
	return res, nil
}

// differs says how the value of the expression key differs from want, or is
// empty if it does not.
func differs(env *expr.Env, key string, want any, vars map[string]any) string {
	got, err := env.EvalExpr(key, vars)
	if err != nil {
		return fmt.Sprintf("%s: %v", key, err)
	}
	if g, w := jsonText(got), jsonText(want); g != w {
		return fmt.Sprintf("%s: expected %s, got %s", key, w, g)
	}
	return ""
}

// unmet lists the expectations that the finished run does not meet.
func unmet(expect map[string]any, steps []StepResult, vars map[string]any) ([]string, error) {
	status := map[string]Status{}
	names := []string{"trigger"}
	for _, s := range steps {
		status[s.ID] = s.Status
		names = append(names, s.ID)
	}
	if _, answered := vars["response"]; answered {
		names = append(names, "response")
	}
	env, err := expr.Vars(names...)
	if err != nil {
		return nil, err
	}

	var failures []string
	for _, key := range slices.Sorted(maps.Keys(expect)) {
		want := expect[key]

		if st, isStep := status[key]; isStep {
			got := workflow.Called
			if st == Skipped {
				got = workflow.Skipped
			}
			if got != want {
				failures = append(failures, fmt.Sprintf("%s: expected %v, but it was %s", key, want, got))
			}
			continue
		}

		got, err := env.EvalExpr(key, vars)
		if err != nil {
			if id, _, _ := strings.Cut(key, "."); status[id] == Skipped {
				err = fmt.Errorf("step %q was skipped", id)
			}
			failures = append(failures, fmt.Sprintf("%s: %v", key, err))
		} else if g, w := jsonText(got), jsonText(want); g != w {
			failures = append(failures, fmt.Sprintf("%s: expected %s, got %s", key, w, g))
		}
	}
	return failures, nil
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

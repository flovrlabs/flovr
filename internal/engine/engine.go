// Package engine runs a workflow's steps in order.
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"aia/internal/ai"
	"aia/internal/expr"
	"aia/internal/workflow"
)

type Status string

const (
	OK      Status = "ok"
	Skipped Status = "skipped"
	Failed  Status = "failed"
)

// StepResult is what one step did.
type StepResult struct {
	ID       string
	Kind     string
	Status   Status
	Output   any    // what later steps see under the step's id; nil if skipped
	Detail   string // one-line summary for people, free of secrets
	Duration time.Duration
	Err      error
}

type Model interface {
	Generate(ctx context.Context, req ai.Request) (ai.Response, error)
	Decide(ctx context.Context, req ai.DecideRequest) (ai.DecideResponse, error)
}

type Engine struct {
	Model  Model
	HTTP   *http.Client
	Env    map[string]string // what expressions see as env
	OnStep func(StepResult)  // called after each step, if set

	// StubHTTP makes http steps prepare their request but not send it. Later
	// steps see an empty 200 response.
	StubHTTP bool
}

// Run executes w with trigger as the triggering event, stopping at the first
// step that fails.
func (e *Engine) Run(ctx context.Context, w *workflow.Workflow, trigger any) error {
	_, err := e.run(ctx, w, trigger)
	return err
}

// run is Run, also returning everything expressions could see by the end:
// trigger, env, and each step's output.
func (e *Engine) run(ctx context.Context, w *workflow.Workflow, trigger any) (map[string]any, error) {
	vars := map[string]any{"trigger": trigger, "env": e.Env}
	var ids []string
	for _, s := range w.Steps {
		env, err := expr.NewEnv(ids)
		if err != nil {
			return vars, err
		}
		res := e.step(ctx, w, s, env, vars)
		if e.OnStep != nil {
			e.OnStep(res)
		}
		if res.Err != nil {
			return vars, fmt.Errorf("step %q failed: %w", s.ID, res.Err)
		}
		vars[s.ID] = res.Output
		ids = append(ids, s.ID)
	}
	return vars, nil
}

func (e *Engine) step(ctx context.Context, w *workflow.Workflow, s workflow.Step, env *expr.Env, vars map[string]any) (res StepResult) {
	res = StepResult{ID: s.ID, Kind: s.Kind(), Status: OK}
	start := time.Now()
	defer func() {
		res.Duration = time.Since(start)
		if res.Err != nil {
			res.Status = Failed
		}
	}()

	if s.When != "" {
		ok, err := env.EvalCondition(s.When, vars)
		if err != nil {
			res.Err = fmt.Errorf("when: %w", err)
			return res
		}
		if !ok {
			res.Status = Skipped
			return res
		}
	}
	switch {
	case s.AI != nil:
		res.Output, res.Detail, res.Err = e.ai(ctx, w, s.AI, env, vars)
	case s.Decide != nil:
		res.Output, res.Detail, res.Err = e.decide(ctx, s.Decide, env, vars)
	case s.HTTP != nil:
		res.Output, res.Detail, res.Err = e.http(ctx, s.HTTP, env, vars)
	case s.Component() != nil:
		res.Output, res.Detail, res.Err = e.use(ctx, s, env, vars)
	}
	return res
}

// use runs the component a step names, giving it the step's with as input.
func (e *Engine) use(ctx context.Context, s workflow.Step, env *expr.Env, vars map[string]any) (any, string, error) {
	input := make(map[string]any, len(s.With))
	for _, name := range slices.Sorted(maps.Keys(s.With)) {
		v, err := env.Eval(s.With[name], vars)
		if err != nil {
			return nil, "", fmt.Errorf("with.%s: %w", name, err)
		}
		input[name] = v
	}
	return e.call(ctx, s.Component(), input)
}

// call runs a component. Its expressions see the input and env, and nothing
// of what called it.
func (e *Engine) call(ctx context.Context, c *workflow.Component, input map[string]any) (any, string, error) {
	if err := c.Input.Check(input); err != nil {
		return nil, "", fmt.Errorf("input of %s: %w", c.Name, err)
	}
	env, err := expr.Vars("input")
	if err != nil {
		return nil, "", err
	}
	vars := map[string]any{"input": input, "env": e.Env}
	switch {
	case c.AI != nil:
		return e.ai(ctx, c, c.AI, env, vars)
	case c.Decide != nil:
		return e.decide(ctx, c.Decide, env, vars)
	}
	return e.http(ctx, c.HTTP, env, vars)
}

// files is a workflow or component file, as far as the paths written in it
// are concerned.
type files interface {
	Resolve(rel string) string
	Schema(rel string) (*jsonschema.Schema, map[string]any, error)
}

func (e *Engine) ai(ctx context.Context, w files, s *workflow.AIStep, env *expr.Env, vars map[string]any) (any, string, error) {
	prompt, err := os.ReadFile(w.Resolve(s.Prompt))
	if err != nil {
		return nil, "", err
	}
	schema, schemaDoc, err := w.Schema(s.Output)
	if err != nil {
		return nil, "", err
	}
	input, err := env.Eval(s.Input, vars)
	if err != nil {
		return nil, "", fmt.Errorf("input: %w", err)
	}
	text, ok := input.(string)
	if !ok {
		b, err := json.MarshalIndent(input, "", "  ")
		if err != nil {
			return nil, "", fmt.Errorf("input: %w", err)
		}
		text = string(b)
	}

	resp, err := e.Model.Generate(ctx, ai.Request{
		Model:  s.Model,
		System: string(prompt),
		Input:  text,
		Schema: schemaDoc,
	})
	if err != nil {
		return nil, "", err
	}
	detail := fmt.Sprintf("%s, %d tokens in, %d out", resp.Model, resp.InputTokens, resp.OutputTokens)

	// The API constrains the answer to the schema, but only for the schema
	// features it supports, so the full schema is checked here as well.
	instance, err := jsonschema.UnmarshalJSON(strings.NewReader(resp.Text))
	if err != nil {
		return nil, detail, fmt.Errorf("the model's answer is not JSON: %w", err)
	}
	if err := schema.Validate(instance); err != nil {
		return nil, detail, fmt.Errorf("the model's answer does not match %s: %w", s.Output, err)
	}
	var out any
	if err := json.Unmarshal([]byte(resp.Text), &out); err != nil {
		return nil, detail, err
	}
	return out, detail, nil
}

func (e *Engine) decide(ctx context.Context, s *workflow.DecideStep, env *expr.Env, vars map[string]any) (any, string, error) {
	state, err := env.Eval(s.Input, vars)
	if err != nil {
		return nil, "", fmt.Errorf("input: %w", err)
	}
	questions := make(map[string]ai.Question, len(s.Questions))
	for name, q := range s.Questions {
		questions[name] = q.Spec()
	}

	resp, err := e.Model.Decide(ctx, ai.DecideRequest{Model: s.Model, State: state, Questions: questions})
	if err != nil {
		return nil, "", err
	}
	return resp.Answers, fmt.Sprintf("%s, %d tokens in", resp.Model, resp.InputTokens), nil
}

func (e *Engine) http(ctx context.Context, s *workflow.HTTPStep, env *expr.Env, vars map[string]any) (any, string, error) {
	target, err := env.EvalString(s.URL, vars)
	if err != nil {
		return nil, "", fmt.Errorf("url: %w", err)
	}
	method := strings.ToUpper(s.Method)

	var body io.Reader
	headers := http.Header{}
	if s.Body != nil {
		v, err := env.Eval(s.Body, vars)
		if err != nil {
			return nil, "", fmt.Errorf("body: %w", err)
		}
		if text, ok := v.(string); ok {
			body = strings.NewReader(text)
		} else {
			b, err := json.Marshal(v)
			if err != nil {
				return nil, "", fmt.Errorf("body: %w", err)
			}
			body = bytes.NewReader(b)
			headers.Set("Content-Type", "application/json")
		}
	}
	for name, tmpl := range s.Headers {
		value, err := env.EvalString(tmpl, vars)
		if err != nil {
			return nil, "", fmt.Errorf("headers.%s: %w", name, err)
		}
		headers.Set(name, value)
	}

	if e.StubHTTP {
		return map[string]any{"status": http.StatusOK, "body": nil}, method + ", not sent", nil
	}

	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, "", errors.New("url is not a valid URL")
	}
	req.Header = headers

	// Webhook URLs often carry a token in the path or query, so only the
	// host is ever shown.
	where := method + " " + req.URL.Scheme + "://" + req.URL.Host
	resp, err := e.HTTP.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, where, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, where, err
	}
	detail := fmt.Sprintf("%s -> %d", where, resp.StatusCode)
	if resp.StatusCode >= 400 {
		return nil, detail, fmt.Errorf("the server answered %s", resp.Status)
	}

	var parsed any
	if json.Unmarshal(data, &parsed) != nil {
		parsed = string(data)
	}
	return map[string]any{"status": resp.StatusCode, "body": parsed}, detail, nil
}

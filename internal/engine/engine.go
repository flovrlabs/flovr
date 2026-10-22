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

	"github.com/flovrlabs/flovr/internal/ai"
	"github.com/flovrlabs/flovr/internal/expr"
	"github.com/flovrlabs/flovr/internal/workflow"
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
	Tries    int    // how many times it ran: more than 1 after a retry, 0 if skipped
	Tokens   Tokens // what its models read and wrote, over every try and every step inside it

	// Steps is what the steps of a component made of steps did, for a step
	// that uses one.
	Steps []StepResult
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

// Response is what a workflow that has respond answers its caller with.
type Response struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    any               `json:"body"`
}

// Run executes w with trigger as the triggering event, stopping at the first
// step that fails. The response is nil for a workflow that has no respond.
func (e *Engine) Run(ctx context.Context, w *workflow.Workflow, trigger any) (*Response, error) {
	_, resp, err := e.run(ctx, w, trigger)
	return resp, err
}

// run is Run, also returning everything expressions could see by the end:
// trigger, env, and each step's output.
func (e *Engine) run(ctx context.Context, w *workflow.Workflow, trigger any) (map[string]any, *Response, error) {
	vars := map[string]any{"trigger": trigger, "env": e.Env}
	if err := w.CheckTrigger(trigger); err != nil {
		return vars, nil, err
	}
	var ids []string
	for _, s := range w.Steps {
		env, err := expr.NewEnv(ids)
		if err != nil {
			return vars, nil, err
		}
		res := e.step(ctx, w, s, env, vars)
		if e.OnStep != nil {
			e.OnStep(res)
		}
		if res.Err != nil {
			return vars, nil, fmt.Errorf("step %q failed: %w", s.ID, res.Err)
		}
		vars[s.ID] = res.Output
		ids = append(ids, s.ID)
	}
	if w.Respond == nil {
		return vars, nil, nil
	}
	resp, err := respond(w.Respond, ids, vars)
	if err != nil {
		return vars, nil, fmt.Errorf("respond: %w", err)
	}
	return vars, resp, nil
}

// RunStep runs the step at position i of w on its own, as a run would have
// run it after the steps before it had put out what earlier holds under their
// ids; a step missing from earlier was skipped. trigger is what started the
// run. Its condition is not checked, so a step can be tried whatever its
// condition says; wouldRun says what the condition gives, and is true for a
// step without one.
func (e *Engine) RunStep(ctx context.Context, w *workflow.Workflow, i int, trigger any, earlier map[string]any) (res StepResult, wouldRun bool) {
	s := w.Steps[i]
	vars := map[string]any{"trigger": trigger, "env": e.Env}
	var ids []string
	for _, before := range w.Steps[:i] {
		ids = append(ids, before.ID)
		vars[before.ID] = earlier[before.ID]
	}
	env, err := expr.NewEnv(ids)
	if err != nil {
		return StepResult{ID: s.ID, Kind: s.Kind(), Status: Failed, Err: err}, false
	}
	wouldRun = true
	if s.When != "" {
		if wouldRun, err = env.EvalCondition(s.When, vars); err != nil {
			return StepResult{ID: s.ID, Kind: s.Kind(), Status: Failed, Err: fmt.Errorf("when: %w", err)}, false
		}
		s.When = ""
	}
	return e.step(ctx, w, s, env, vars), wouldRun
}

// respond works out the answer to the caller from what the steps produced.
func respond(r *workflow.Respond, ids []string, vars map[string]any) (*Response, error) {
	env, err := expr.NewEnv(ids)
	if err != nil {
		return nil, err
	}
	resp := &Response{Status: r.Status}
	if resp.Status == 0 {
		resp.Status = http.StatusOK
	}
	for _, name := range slices.Sorted(maps.Keys(r.Headers)) {
		value, err := env.EvalString(r.Headers[name], vars)
		if err != nil {
			return nil, fmt.Errorf("headers.%s: %w", name, err)
		}
		if resp.Headers == nil {
			resp.Headers = map[string]string{}
		}
		resp.Headers[name] = value
	}
	if resp.Body, err = env.Eval(r.Body, vars); err != nil {
		return nil, fmt.Errorf("body: %w", err)
	}
	return resp, nil
}

// step runs one step. f is the file the step is written in, a workflow's or
// a component's.
func (e *Engine) step(ctx context.Context, f files, s workflow.Step, env *expr.Env, vars map[string]any) (res StepResult) {
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
	e.do(ctx, f, s, env, vars, &res)
	return res
}

// use runs the component a step names, giving it the step's with as input.
func (e *Engine) use(ctx context.Context, s workflow.Step, env *expr.Env, vars map[string]any) (any, string, []StepResult, error) {
	input := make(map[string]any, len(s.With))
	for _, name := range slices.Sorted(maps.Keys(s.With)) {
		v, err := env.Eval(s.With[name], vars)
		if err != nil {
			return nil, "", nil, fmt.Errorf("with.%s: %w", name, err)
		}
		input[name] = v
	}
	out, detail, inner, _, err := e.call(ctx, s.Component(), input)
	return out, detail, inner, err
}

// call runs a component. Its expressions see the input and env, and nothing
// of what called it. For a component made of steps it also returns what each
// step did and everything its expressions could see by the end.
func (e *Engine) call(ctx context.Context, c *workflow.Component, input map[string]any) (out any, detail string, inner []StepResult, vars map[string]any, err error) {
	vars = map[string]any{"input": input, "env": e.Env}
	if err := c.Input.Check(input); err != nil {
		return nil, "", nil, vars, fmt.Errorf("input of %s: %w", c.Name, err)
	}
	if len(c.Steps) == 0 {
		env, err := expr.Vars("input")
		if err != nil {
			return nil, "", nil, vars, err
		}
		switch {
		case c.AI != nil:
			out, detail, err = e.ai(ctx, c, c.AI, env, vars)
		case c.Decide != nil:
			out, detail, err = e.decide(ctx, c.Decide, env, vars)
		default:
			out, detail, err = e.http(ctx, c.HTTP, env, vars)
		}
		return out, detail, nil, vars, err
	}

	names := []string{"input"}
	var ran, skipped []string
	for _, s := range c.Steps {
		env, err := expr.Vars(names...)
		if err != nil {
			return nil, "", inner, vars, err
		}
		res := e.step(ctx, c, s, env, vars)
		inner = append(inner, res)
		if res.Err != nil {
			return nil, "stopped at " + s.ID, inner, vars, fmt.Errorf("step %q of %s failed: %w", s.ID, c.Name, res.Err)
		}
		if res.Status == Skipped {
			skipped = append(skipped, s.ID)
		} else {
			ran = append(ran, s.ID)
		}
		vars[s.ID] = res.Output
		names = append(names, s.ID)
	}
	detail = "ran " + strings.Join(ran, ", ")
	if len(ran) == 0 {
		detail = "ran no step"
	}
	if len(skipped) > 0 {
		detail += "; skipped " + strings.Join(skipped, ", ")
	}
	env, err := expr.Vars(names...)
	if err != nil {
		return nil, detail, inner, vars, err
	}
	if out, err = env.Eval(c.Output, vars); err != nil {
		return nil, detail, inner, vars, fmt.Errorf("output of %s: %w", c.Name, err)
	}
	return out, detail, inner, vars, nil
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
	if strings.TrimSpace(resp.Text) == "" {
		return nil, detail, &InvalidAnswerError{resp.Text, errors.New("the model gave an empty answer")}
	}
	instance, err := jsonschema.UnmarshalJSON(strings.NewReader(resp.Text))
	if err != nil {
		return nil, detail, &InvalidAnswerError{resp.Text, fmt.Errorf("the model's answer is not JSON: %w", err)}
	}
	if err := schema.Validate(instance); err != nil {
		if problems := workflow.SchemaProblems(err, "the answer"); problems != nil {
			err = errors.New(strings.Join(problems, "; "))
		}
		return nil, detail, &InvalidAnswerError{resp.Text, fmt.Errorf("the model's answer does not match %s: %w", s.Output, err)}
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
		return nil, detail, &HTTPStatusError{resp.StatusCode, resp.Status}
	}

	var parsed any
	if json.Unmarshal(data, &parsed) != nil {
		parsed = string(data)
	}
	return map[string]any{"status": resp.StatusCode, "body": parsed}, detail, nil
}

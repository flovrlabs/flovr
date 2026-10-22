package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/flovrlabs/flovr/internal/expr"
)

// Respond is what the caller of a workflow's webhook gets back once the
// steps have run. A workflow that has it is an endpoint: the request waits
// for the answer.
type Respond struct {
	Status  int               `yaml:"status"` // 200 if not given
	Headers map[string]string `yaml:"headers"`
	Body    any               `yaml:"body"`
}

// expressions returns the inside of each ${...} placeholder of the response.
func (r *Respond) expressions() []string {
	if r == nil {
		return nil
	}
	exprs := expr.Placeholders(r.Body)
	for _, value := range r.Headers {
		exprs = append(exprs, expr.Placeholders(value)...)
	}
	return exprs
}

// InputError says how a request body differs from what a workflow takes.
type InputError struct {
	Problems []string // each names the place in the body and what is wrong there
}

func (e *InputError) Error() string {
	return "the body does not match what the workflow takes: " + strings.Join(e.Problems, "; ")
}

// CheckTrigger reports whether the event that starts the workflow has a body
// matching the schema the trigger names as its input. The error is an
// *InputError if it does not. A workflow that names no schema takes anything.
func (w *Workflow) CheckTrigger(trigger any) error {
	if w.Trigger == nil || w.Trigger.Input == "" {
		return nil
	}
	schema, _, err := w.Schema(w.Trigger.Input)
	if err != nil {
		return err
	}
	var body any
	if event, ok := trigger.(map[string]any); ok {
		body = event["body"]
	}
	return matches(schema, body)
}

// SchemaProblems lists what a JSON Schema found wrong with a value, each as where
// in the value and what, with whole standing for the value itself. It is
// nil when err is not a validation error.
func SchemaProblems(err error, whole string) []string {
	var invalid *jsonschema.ValidationError
	if !errors.As(err, &invalid) {
		return nil
	}
	var problems []string
	for _, unit := range invalid.BasicOutput().Errors {
		if unit.Error == nil {
			continue
		}
		where := unit.InstanceLocation
		if where == "" {
			where = whole
		}
		problems = append(problems, fmt.Sprintf("%s: %s", where, unit.Error))
	}
	return problems
}

// matches checks v, as decoded from JSON or YAML, against schema.
func matches(schema *jsonschema.Schema, v any) error {
	// The validator wants numbers the way its own decoder reads them.
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}
	err = schema.Validate(instance)
	var invalid *jsonschema.ValidationError
	if !errors.As(err, &invalid) {
		return err
	}
	e := &InputError{Problems: SchemaProblems(err, "the body")}
	if len(e.Problems) == 0 {
		e.Problems = []string{err.Error()}
	}
	return e
}

package workflow

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"aia/internal/expr"
)

// shape is what is known of the fields of a value that expressions read. A
// nil shape is a value of which nothing is known, so that anything may be
// read from it.
type shape struct {
	fields map[string]*shape
	open   bool // it may have fields besides those known
}

// single is the shape of a value without fields, such as text or a number.
func single() *shape { return &shape{} }

// misread says what is wrong with reading the fields, one below the other,
// from a value of this shape called name. It is empty if nothing is known to
// be wrong.
func (s *shape) misread(name string, fields []string) string {
	for _, field := range fields {
		if s == nil {
			return ""
		}
		next, ok := s.fields[field]
		switch {
		case ok:
		case s.open:
			return ""
		case len(s.fields) == 0:
			return fmt.Sprintf("%s is a single value, so there is no %s.%s", name, name, field)
		default:
			return fmt.Sprintf("%s has no %s; it has %s", name, field, strings.Join(slices.Sorted(maps.Keys(s.fields)), ", "))
		}
		s, name = next, name+"."+field
	}
	return ""
}

// schemaShape is the shape of a value that matches a JSON Schema, as far as
// the schema spells it out plainly.
//
// A schema allows properties it does not list unless it says otherwise, and
// a request body may well carry some. What a model writes to a schema holds
// the listed ones only, so for that closed is true: reading any other is a
// mistake, unless the schema itself says more are allowed.
func schemaShape(schema any, closed bool) *shape {
	doc, ok := schema.(map[string]any)
	if !ok {
		return nil
	}
	// A schema put together from others says more than is read here.
	for _, key := range []string{"$ref", "allOf", "anyOf", "oneOf", "if", "patternProperties"} {
		if _, combined := doc[key]; combined {
			return nil
		}
	}
	switch doc["type"] {
	case "string", "number", "integer", "boolean":
		return single()
	case "object":
		properties, ok := doc["properties"].(map[string]any)
		if !ok {
			return nil
		}
		s := &shape{fields: map[string]*shape{}, open: !closed}
		for name, property := range properties {
			s.fields[name] = schemaShape(property, closed)
		}
		if more, given := doc["additionalProperties"]; given {
			s.open = more != false
		}
		return s
	}
	return nil
}

// decideShape is the shape of what a decide step puts out: an answer for
// each question, in the form its kind of question gives.
func decideShape(d *DecideStep) *shape {
	s := &shape{fields: map[string]*shape{}}
	for name, q := range d.Questions {
		answer := &shape{fields: map[string]*shape{"value": single()}}
		options := q.Choice
		if q.Score != nil {
			options = Options(q.Score)
			answer.fields["score"] = single()
		}
		if q.Choice == nil && q.Score == nil {
			answer.fields["probability"] = single()
		} else {
			each := &shape{fields: map[string]*shape{}}
			for _, o := range options {
				each.fields[o.Name] = single()
			}
			answer.fields["confidence"], answer.fields["probabilities"] = single(), each
		}
		s.fields[name] = answer
	}
	return s
}

// outputShape is the shape of what a step or component of the given work
// puts out. file is the file it is written in.
func outputShape(file string, ai *AIStep, decide *DecideStep, http *HTTPStep) *shape {
	switch {
	case ai != nil:
		if _, doc, err := loadSchema(file, ai.Output); err == nil {
			return schemaShape(doc, true)
		}
	case decide != nil:
		return decideShape(decide)
	case http != nil:
		return &shape{fields: map[string]*shape{"status": single(), "body": nil}}
	}
	return nil
}

// responseShape is the shape of the answer a workflow gives, as tests see it.
func responseShape(r *Respond) *shape {
	s := &shape{fields: map[string]*shape{"status": single(), "headers": nil, "body": nil}}
	switch body := r.Body.(type) {
	case string:
		// One placeholder and nothing else keeps the type of its value.
		if !expr.Single(body) {
			s.fields["body"] = single()
		}
	case map[string]any:
		fields := &shape{fields: map[string]*shape{}}
		for name := range body {
			fields.fields[name] = nil
		}
		s.fields["body"] = fields
	}
	return s
}

// inputShape is the shape of what a component is given.
func inputShape(params Params) *shape {
	s := &shape{fields: map[string]*shape{}}
	for _, p := range params {
		switch p.Type {
		case "string", "number", "boolean":
			s.fields[p.Name] = single()
		default:
			s.fields[p.Name] = nil
		}
	}
	return s
}

// reads reports the fields that the expressions read from values known not
// to have them. shapes holds what is known, by the name expressions use.
func (c *checker) reads(line int, label string, exprs []string, shapes map[string]*shape) {
	said := map[string]bool{}
	for _, src := range exprs {
		for _, path := range expr.Reads(src) {
			known, ok := shapes[path[0]]
			if !ok {
				continue
			}
			if msg := known.misread(path[0], path[1:]); msg != "" && !said[msg] {
				said[msg] = true
				c.addf(line, "%s: %s", label, msg)
			}
		}
	}
}

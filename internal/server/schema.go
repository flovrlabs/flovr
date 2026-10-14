package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/simonyosb/flowmaker/internal/workflow"
)

// schemaField is one field of the answer an ai step must give, as the page
// shows and edits it.
type schemaField struct {
	Name string `json:"name"`
	// Type is "text", "number", "whole number", "yes or no", "list of text"
	// or "one of", which takes Options.
	Type        string   `json:"type"`
	Options     []string `json:"options,omitempty"`
	Required    bool     `json:"required"`
	Description string   `json:"description,omitempty"`
}

// schemaView is the output schema of an ai step.
type schemaView struct {
	File string `json:"file"` // as the step names it
	Text string `json:"text"`
	Rev  string `json:"rev"`
	// Fields is the schema as a list of fields, or null when it says more
	// than such a list can, in which case the page edits its text.
	Fields []schemaField `json:"fields"`
}

type schemaRequest struct {
	Workflow string        `json:"workflow"`
	Step     string        `json:"step"`
	Rev      string        `json:"rev"` // the file as the page last saw it
	Fields   []schemaField `json:"fields"`
	Text     *string       `json:"text"` // the whole schema, instead of Fields
}

var fieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// fieldTypes maps the page's names of field types to JSON Schema.
var fieldTypes = map[string]string{"text": "string", "number": "number", "whole number": "integer", "yes or no": "boolean"}

// schemaFile finds the output schema file of an ai step of a workflow. The
// page reads and writes such files only inside the workflow's folder.
func (s *Server) schemaFile(name, step string) (*workflow.Workflow, string, string, int, error) {
	for _, w := range s.live.Load().workflows {
		if w.Name != name {
			continue
		}
		for _, st := range w.Steps {
			if st.ID != step || st.AI == nil {
				continue
			}
			path := w.Resolve(st.AI.Output)
			if !within(filepath.Dir(w.Path), path) {
				return nil, "", "", http.StatusForbidden, fmt.Errorf("%s is outside the workflow's folder, so the page does not open it", st.AI.Output)
			}
			return w, path, st.AI.Output, 0, nil
		}
	}
	return nil, "", "", http.StatusNotFound, fmt.Errorf("no ai step %s in a workflow named %s", step, name)
}

func (s *Server) schema(rw http.ResponseWriter, r *http.Request) {
	_, path, written, status, err := s.schemaFile(r.URL.Query().Get("workflow"), r.URL.Query().Get("step"))
	if err != nil {
		reply(rw, status, fail("%v", err))
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		reply(rw, http.StatusInternalServerError, fail("%v", err))
		return
	}
	reply(rw, http.StatusOK, schemaView{File: written, Text: string(data), Rev: revision(data), Fields: fieldsOf(data)})
}

// saveSchema replaces the output schema of an ai step, given as fields or as
// text. As with every change, nothing is written unless every workflow that
// uses the file is still one the server can serve; a later step that reads a
// field taken away is a reason to refuse.
func (s *Server) saveSchema(req schemaRequest) (int, any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, path, written, status, err := s.schemaFile(req.Workflow, req.Step)
	if err != nil {
		return status, fail("%v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return http.StatusInternalServerError, fail("%v", err)
	}
	if revision(data) != req.Rev {
		return http.StatusConflict, fail(stale)
	}

	var out []byte
	if req.Text != nil {
		var doc map[string]any
		if err := json.Unmarshal([]byte(*req.Text), &doc); err != nil {
			return http.StatusUnprocessableEntity, fail("The schema is not a JSON object: %v", err)
		}
		out = []byte(strings.TrimRight(*req.Text, " \t\r\n") + "\n")
	} else {
		if out, err = schemaOf(req.Fields, data); err != nil {
			return http.StatusUnprocessableEntity, fail("%v", err)
		}
	}
	if bytes.Equal(out, data) {
		return http.StatusOK, schemaView{File: written, Text: string(out), Rev: revision(out), Fields: fieldsOf(out)}
	}

	// Every workflow that names the file is checked with what it would hold.
	files := workflow.Overlay{path: out}
	live := s.live.Load()
	next := slices.Clone(live.workflows)
	for i, w := range live.workflows {
		if !names(w, path) {
			continue
		}
		text, err := os.ReadFile(w.Path)
		if err != nil {
			return http.StatusInternalServerError, fail("%v", err)
		}
		if next[i], err = workflow.ParseWith(w.Path, text, files); err != nil {
			return http.StatusUnprocessableEntity, invalid("Nothing was saved, because "+w.Name+" would no longer be valid.", err)
		}
	}
	if _, err := route(next, s.opts.Env); err != nil {
		return http.StatusUnprocessableEntity, invalid("Nothing was saved, because the server could no longer serve its workflows.", err)
	}
	if err := writeFile(path, out); err != nil {
		return http.StatusInternalServerError, fail("%v", err)
	}
	s.serve(next)
	s.opts.Log("%s changed from the page: the output schema of %s (%s)", req.Workflow, req.Step, written)
	return http.StatusOK, schemaView{File: written, Text: string(out), Rev: revision(out), Fields: fieldsOf(out)}
}

// names reports whether w names the schema file at path, as an ai step's
// output or as the trigger's input.
func names(w *workflow.Workflow, path string) bool {
	same := func(rel string) bool { return rel != "" && filepath.Clean(w.Resolve(rel)) == filepath.Clean(path) }
	if w.Trigger != nil && same(w.Trigger.Input) {
		return true
	}
	return slices.ContainsFunc(w.Steps, func(st workflow.Step) bool { return st.AI != nil && same(st.AI.Output) })
}

// fieldsOf reads a schema as a list of fields, if it is one: an object with
// properties of the kinds schemaField knows, and nothing else. Otherwise it
// returns nil.
func fieldsOf(data []byte) []schemaField {
	var doc map[string]any
	if json.Unmarshal(data, &doc) != nil || doc["type"] != "object" {
		return nil
	}
	for key, v := range doc {
		switch key {
		case "$schema", "type", "properties", "required":
		case "additionalProperties":
			if v != false {
				return nil
			}
		default:
			return nil
		}
	}
	props, _ := doc["properties"].(map[string]any)
	required := map[string]bool{}
	if list, ok := doc["required"].([]any); ok {
		for _, name := range list {
			text, ok := name.(string)
			if !ok {
				return nil
			}
			required[text] = true
		}
	}
	fields := []schemaField{}
	for _, name := range propertyOrder(data) {
		prop, ok := props[name].(map[string]any)
		if !ok {
			return nil
		}
		f := schemaField{Name: name, Required: required[name]}
		for key, v := range prop {
			switch key {
			case "type", "enum", "items":
			case "description":
				text, ok := v.(string)
				if !ok {
					return nil
				}
				f.Description = text
			default:
				return nil
			}
		}
		kind, _ := prop["type"].(string)
		enum, hasEnum := prop["enum"].([]any)
		items, hasItems := prop["items"].(map[string]any)
		switch {
		case hasEnum && kind == "string" && !hasItems:
			f.Type = "one of"
			for _, o := range enum {
				text, ok := o.(string)
				if !ok {
					return nil
				}
				f.Options = append(f.Options, text)
			}
		case kind == "array" && hasItems && !hasEnum && len(items) == 1 && items["type"] == "string":
			f.Type = "list of text"
		case !hasEnum && !hasItems:
			for name, schemaType := range fieldTypes {
				if schemaType == kind {
					f.Type = name
				}
			}
			if f.Type == "" {
				return nil
			}
		default:
			return nil
		}
		fields = append(fields, f)
	}
	if len(fields) != len(props) {
		return nil
	}
	return fields
}

// propertyOrder returns the names of the top-level properties in the order
// the file lists them.
func propertyOrder(data []byte) []string {
	var top map[string]json.RawMessage
	if json.Unmarshal(data, &top) != nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(top["properties"]))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	var names []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil
		}
		name, _ := tok.(string)
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return nil
		}
		names = append(names, name)
	}
	return names
}

// schemaOf writes fields as a JSON Schema, keeping the $schema the old
// schema named.
func schemaOf(fields []schemaField, old []byte) ([]byte, error) {
	var was map[string]any
	json.Unmarshal(old, &was)
	dialect, _ := was["$schema"].(string)
	if dialect == "" {
		dialect = "https://json-schema.org/draft/2020-12/schema"
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("The answer needs at least one field.")
	}
	props := ordered{}
	required := []string{}
	seen := map[string]bool{}
	for _, f := range fields {
		name := strings.TrimSpace(f.Name)
		switch {
		case !fieldName.MatchString(name):
			return nil, fmt.Errorf("%q cannot be a field's name: use letters, digits and _, not starting with a digit.", name)
		case seen[name]:
			return nil, fmt.Errorf("There are two fields named %s.", name)
		}
		seen[name] = true
		prop := ordered{}
		switch f.Type {
		case "one of":
			var options []string
			for _, o := range f.Options {
				if o = strings.TrimSpace(o); o != "" {
					options = append(options, o)
				}
			}
			if len(options) < 2 {
				return nil, fmt.Errorf("%s is one of a list, so it needs at least two options.", name)
			}
			prop = append(prop, kv{"type", "string"}, kv{"enum", options})
		case "list of text":
			prop = append(prop, kv{"type", "array"}, kv{"items", ordered{{"type", "string"}}})
		default:
			kind, ok := fieldTypes[f.Type]
			if !ok {
				return nil, fmt.Errorf("%s has an unknown type %q.", name, f.Type)
			}
			prop = append(prop, kv{"type", kind})
		}
		if d := strings.TrimSpace(f.Description); d != "" {
			prop = append(prop, kv{"description", d})
		}
		props = append(props, kv{name, prop})
		if f.Required {
			required = append(required, name)
		}
	}
	doc := ordered{{"$schema", dialect}, {"type", "object"}, {"properties", props}, {"required", required}, {"additionalProperties", false}}
	flat, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, flat, "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// ordered is a JSON object that keeps the order of its keys.
type ordered []kv

type kv struct {
	k string
	v any
}

func (o ordered) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	buf.WriteByte('{')
	for i, p := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := enc.Encode(p.k); err != nil {
			return nil, err
		}
		buf.Truncate(buf.Len() - 1)
		buf.WriteByte(':')
		if err := enc.Encode(p.v); err != nil {
			return nil, err
		}
		buf.Truncate(buf.Len() - 1)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

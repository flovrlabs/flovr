package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"aia/internal/workflow"
	"aia/internal/yamledit"
)

// casesRequest drafts a test from a run, adds a test to a workflow, or
// removes one.
type casesRequest struct {
	Workflow string `json:"workflow"`
	Rev      string `json:"rev"` // the file as the page last saw it; not needed to draft

	// Draft is the id of a run to draft a test from. Nothing is written.
	Draft string `json:"draft"`

	Add *struct {
		Name string `json:"name"`
		// Fixture is the event the test starts the workflow with, such as
		// {"body": {...}} for a webhook.
		Fixture any `json:"fixture"`
		Expect  []struct {
			Key  string `json:"key"`
			Want any    `json:"want"`
		} `json:"expect"`
	} `json:"add"`

	Remove *int `json:"remove"` // the position of the test to remove, counted from 0
}

// draftView is a test drafted from a run, for a person to trim.
type draftView struct {
	Fixture any          `json:"fixture"`
	Expect  []draftCheck `json:"expect"`
}

// draftCheck is an expectation that a run would meet.
type draftCheck struct {
	Key  string `json:"key"`
	Want any    `json:"want"`
	// Keep says whether the expectation is one to rely on. What a model
	// decided among fixed answers is; text it wrote will differ next time.
	Keep bool `json:"keep"`
}

// cases drafts, adds or removes a test of a workflow.
func (s *Server) cases(req casesRequest) (int, any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.componentNamed(req.Workflow) != nil {
		return http.StatusUnprocessableEntity, fail("The tests of a component are written in its file.")
	}
	if req.Draft != "" {
		return s.draft(req)
	}
	i, w, data, status, refused := s.open(req.Workflow, req.Rev)
	if status != 0 {
		return status, refused
	}

	if req.Remove != nil {
		at := *req.Remove
		if at < 0 || at >= len(w.Tests) {
			return http.StatusUnprocessableEntity, fail("%s has no test at position %d", w.Name, at)
		}
		var out []byte
		var err error
		if len(w.Tests) == 1 {
			// The last test takes the list along.
			out, err = yamledit.Apply(data, yamledit.Edit{Path: []any{"tests"}, Remove: true})
		} else {
			out, err = yamledit.RemoveItem(data, []any{"tests"}, at)
		}
		if err != nil {
			return http.StatusUnprocessableEntity, fail("%v", err)
		}
		// The fixture stays: another test may use it, and it is the
		// person's file to delete.
		return s.save(i, out, fmt.Sprintf("test %q removed", testName(w.Tests[at])))
	}

	add := req.Add
	if add == nil {
		return http.StatusBadRequest, fail("the request drafts a test, adds one or removes one")
	}
	name := strings.TrimSpace(add.Name)
	switch {
	case name == "":
		return http.StatusUnprocessableEntity, fail("The test needs a name.")
	case add.Fixture == nil:
		return http.StatusUnprocessableEntity, fail("The test needs the event that starts the workflow, as JSON.")
	case len(add.Expect) == 0:
		return http.StatusUnprocessableEntity, fail("The test needs at least one thing to expect.")
	}
	var expect []yamledit.Pair
	for _, e := range add.Expect {
		key := strings.TrimSpace(e.Key)
		switch want := e.Want.(type) {
		case string, bool, float64:
			if slices.ContainsFunc(expect, func(p yamledit.Pair) bool { return p.Key == key }) {
				return http.StatusUnprocessableEntity, fail("%s is expected twice.", key)
			}
			expect = append(expect, yamledit.Pair{Key: key, Value: whole(want)})
		default:
			return http.StatusUnprocessableEntity, fail("What is expected of %s must be text, a number, or true or false.", key)
		}
	}

	// The fixture is a new file in the workflow's folder, named after the
	// test. It has to be there before the workflow that names it is checked.
	dir := filepath.Dir(w.Path)
	rel, err := newFixture(dir, name, add.Fixture)
	if err != nil {
		return http.StatusInternalServerError, fail("%v", err)
	}
	text, err := yamledit.Mapping([]yamledit.Pair{
		{Key: "name", Value: name},
		{Key: "fixture", Value: filepath.ToSlash(rel)},
		{Key: "expect", Value: expect},
	})
	if err != nil {
		os.Remove(filepath.Join(dir, rel))
		return http.StatusUnprocessableEntity, fail("%v", err)
	}
	var out []byte
	if len(w.Tests) == 0 {
		out = appendTests(data, text)
	} else {
		out, err = yamledit.InsertItem(data, []any{"tests"}, len(w.Tests), text)
	}
	if err != nil {
		os.Remove(filepath.Join(dir, rel))
		return http.StatusUnprocessableEntity, fail("%v", err)
	}
	status, answer := s.save(i, out, fmt.Sprintf("test %q added, with %s", name, filepath.ToSlash(rel)))
	if status != http.StatusOK {
		os.Remove(filepath.Join(dir, rel))
	}
	return status, answer
}

// whole turns a number that came as JSON into a whole number where it is one,
// so that 200 is written as 200.
func whole(v any) any {
	if f, ok := v.(float64); ok && f == float64(int64(f)) {
		return int(f)
	}
	return v
}

// appendTests adds a tests list holding one test to a workflow file that has
// none, at its end. item is the test as yamledit.Mapping writes it.
func appendTests(data []byte, item string) []byte {
	var buf bytes.Buffer
	buf.Write(bytes.TrimRight(data, "\n"))
	buf.WriteString("\n\ntests:\n")
	for i, line := range strings.Split(strings.TrimRight(item, "\n"), "\n") {
		switch {
		case i == 0:
			buf.WriteString("  - " + line + "\n")
		case line == "":
			buf.WriteString("\n")
		default:
			buf.WriteString("    " + line + "\n")
		}
	}
	return buf.Bytes()
}

var notSlug = regexp.MustCompile(`[^a-z0-9]+`)

// newFixture writes the event as a new JSON file under fixtures in dir, named
// after the test, and returns its path from dir. It never replaces a file.
func newFixture(dir, name string, event any) (string, error) {
	data, err := json.MarshalIndent(event, "", "  ")
	if err != nil {
		return "", err
	}
	slug := strings.Trim(notSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(slug) > 60 {
		slug = strings.Trim(slug[:60], "-")
	}
	if slug == "" {
		slug = "test"
	}
	if err := os.MkdirAll(filepath.Join(dir, "fixtures"), 0o755); err != nil {
		return "", err
	}
	for n := 1; n < 1000; n++ {
		rel := filepath.Join("fixtures", slug+".json")
		if n > 1 {
			rel = filepath.Join("fixtures", slug+"-"+strconv.Itoa(n)+".json")
		}
		f, err := os.OpenFile(filepath.Join(dir, rel), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, err = f.Write(append(data, '\n'))
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			os.Remove(filepath.Join(dir, rel))
			return "", err
		}
		return rel, nil
	}
	return "", fmt.Errorf("too many fixtures are named %s", slug)
}

// draft proposes a test from a run of the workflow: the event that started
// the run as the fixture, and what the run did as what to expect. The caller
// holds s.mu.
func (s *Server) draft(req casesRequest) (int, any) {
	live := s.live.Load()
	i := slices.IndexFunc(live.workflows, func(w *workflow.Workflow) bool { return w.Name == req.Workflow })
	if i < 0 {
		return http.StatusNotFound, fail("no workflow is named %s", req.Workflow)
	}
	w := live.workflows[i]
	run, ok := s.history.get(req.Draft)
	switch {
	case !ok || run.Workflow != w.Name:
		return http.StatusNotFound, fail("%s has no run %s", w.Name, req.Draft)
	case run.Status != "ok":
		return http.StatusUnprocessableEntity, fail("Only a run that succeeded can become a test: what a failed run did is not what to expect.")
	case run.Trigger == nil:
		return http.StatusUnprocessableEntity, fail("The run's record does not hold the event that started it.")
	}

	view := draftView{Fixture: run.Trigger, Expect: []draftCheck{}}
	add := func(key string, want any, keep bool) {
		view.Expect = append(view.Expect, draftCheck{key, want, keep})
	}
	for _, st := range w.Steps {
		at := slices.IndexFunc(run.Steps, func(r Step) bool { return r.ID == st.ID })
		if at < 0 {
			// The step was added after the run.
			continue
		}
		done := run.Steps[at]
		if done.Status == "skipped" {
			add(st.ID, workflow.Skipped, true)
			continue
		}
		add(st.ID, workflow.Called, true)
		out, _ := done.Output.(map[string]any)
		for _, name := range slices.Sorted(maps.Keys(out)) {
			switch v := out[name].(type) {
			case map[string]any:
				// An answer of a decide step: what it decided is worth
				// holding it to.
				if value, has := v["value"]; has && done.Kind == "decide" && plain(value) {
					add(st.ID+"."+name+".value", value, true)
				}
			default:
				// What an http step would have answered is not known in a
				// test, where it is not sent.
				if plain(v) && done.Kind != "http" {
					add(st.ID+"."+name, v, false)
				}
			}
		}
	}
	if run.Response != nil && w.Respond != nil {
		add("response.status", run.Response.Status, true)
		body, _ := run.Response.Body.(map[string]any)
		for _, name := range slices.Sorted(maps.Keys(body)) {
			if v := body[name]; plain(v) {
				add("response.body."+name, v, label(v))
			}
		}
	}
	return http.StatusOK, map[string]any{"draft": view}
}

// plain reports whether v is a single value a test can expect.
func plain(v any) bool {
	switch v.(type) {
	case string, bool, float64, int, int64:
		return true
	}
	return false
}

// label reports whether v looks like one of a fixed set of answers and not
// like text a model wrote, which would differ from run to run.
func label(v any) bool {
	text, isText := v.(string)
	return !isText || len(text) <= 40 && !strings.ContainsAny(text, " \n")
}

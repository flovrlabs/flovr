package server

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/flovrlabs/flovr/internal/engine"
	"github.com/flovrlabs/flovr/internal/workflow"
	"github.com/flovrlabs/flovr/internal/yamledit"
)

// components returns every component the page shows, by the name the page
// knows it by, and the steps that use each. They are the components that the
// served workflows use and, on a server run for editing, the others in the
// folders it was started with and in the workflows' folders, from which a
// step could be added.
func (s *Server) components() (map[string]*workflow.Component, map[string][]useView) {
	found := map[string]*workflow.Component{}
	uses := map[string][]useView{}
	var take func(c *workflow.Component)
	take = func(c *workflow.Component) {
		id := componentID(c)
		if _, known := found[id]; known {
			return
		}
		found[id] = c
		for _, st := range c.Steps {
			if in := st.Component(); in != nil {
				take(in)
				uses[componentID(in)] = append(uses[componentID(in)], useView{Workflow: id, Step: st.ID})
			}
		}
	}

	workflows := s.live.Load().workflows
	for _, w := range workflows {
		for _, st := range w.Steps {
			if c := st.Component(); c != nil {
				take(c)
				uses[componentID(c)] = append(uses[componentID(c)], useView{Workflow: w.Name, Step: st.ID})
			}
		}
	}
	if s.opts.Edit {
		for _, path := range s.rootFiles() {
			if c, err := workflow.LoadComponent(path); err == nil {
				take(c)
			}
		}
		for _, w := range workflows {
			dir := filepath.Dir(w.Path)
			// A folder below one already searched holds nothing new.
			if slices.ContainsFunc(s.opts.Roots, func(root string) bool { return below(root, dir) }) {
				continue
			}
			for _, c := range workflow.ComponentsIn(dir) {
				take(c)
			}
		}
	}
	// A name stands for one thing on the page, and workflows come first.
	for _, w := range workflows {
		delete(found, w.Name)
	}
	return found, uses
}

// searchEvery is how long the server goes on what it found the last time it
// looked through the folders it was started with.
const searchEvery = 5 * time.Second

// rootFiles returns the component files in the folders the server was
// started with. Such a folder can hold a great deal besides workflows, and
// the page asks every few seconds, so the folders are looked through again
// only after a while. A file that has gone since is passed over when read.
func (s *Server) rootFiles() []string {
	f := &s.found
	f.Lock()
	defer f.Unlock()
	if f.at.IsZero() || time.Since(f.at) > searchEvery {
		f.files = nil
		for _, root := range s.opts.Roots {
			f.files = append(f.files, workflow.ComponentFiles(root)...)
		}
		f.at = time.Now()
	}
	return f.files
}

// componentView describes a component for the page, in the form of a
// workflow.
func (s *Server) componentView(c *workflow.Component, uses []useView) workflowView {
	view := describeComponent(c)
	view.UsedBy = uses
	// One from git is changed at its source, so the page is not told what
	// could be changed either.
	if !s.opts.Edit || workflow.FromGit(c.Path) {
		// Nothing can be changed, so the page is not told what could be.
		for i := range view.Steps {
			view.Steps[i].Edit = nil
		}
		if view.Gives != nil {
			view.Gives.Edit = nil
		}
		return view
	}
	view.Cases = componentCases(c)
	if data, err := os.ReadFile(c.Path); err != nil {
		view.Problem = "the file cannot be read: " + err.Error()
	} else {
		view.Rev = revision(data)
	}
	// What is shown is the component as loaded. If the file has since become
	// one that cannot be loaded, the page says so and does not change it.
	if _, err := workflow.LoadComponent(c.Path); err != nil && view.Problem == "" {
		view.Problem = err.Error()
	}
	return view
}

// editComponent changes values in a component file. The file is written only
// if the component is still valid and every workflow that uses it is still
// one the server can serve. The caller holds s.mu.
func (s *Server) editComponent(c *workflow.Component, req editRequest) (int, any) {
	if workflow.FromGit(c.Path) {
		return http.StatusUnprocessableEntity, fail(fromGit, componentID(c))
	}
	s.refresh()
	id := componentID(c)
	data, err := os.ReadFile(c.Path)
	switch {
	case err != nil:
		return http.StatusInternalServerError, fail("%v", err)
	case revision(data) != req.Rev:
		return http.StatusConflict, fail(stale)
	}
	if _, err := workflow.LoadComponent(c.Path); err != nil {
		return http.StatusConflict, fail("%s has a problem that must be fixed in the file before the page can change it: %v", c.Path, err)
	}

	owner := func(path []any) string {
		switch path[0] {
		case "steps":
			if i, ok := path[1].(int); len(path) > 1 && ok && i >= 0 && i < len(c.Steps) {
				return c.Steps[i].ID
			}
			return ""
		case "output":
			return "output"
		}
		return c.Name
	}
	edits, changed, status, refused := editsFor(describeComponent(c), owner, req)
	if status != 0 {
		return status, refused
	}
	out, err := yamledit.Apply(data, edits...)
	if err != nil {
		return http.StatusUnprocessableEntity, fail("%v", err)
	}
	if bytes.Equal(out, data) {
		return http.StatusOK, map[string]any{"workflow": s.componentView(c, nil)}
	}

	files := workflow.Overlay{c.Path: out}
	if _, err := workflow.LoadComponentWith(c.Path, files); err != nil {
		return http.StatusUnprocessableEntity, invalid("Nothing was saved, because the component would no longer be valid.", err)
	}
	live := s.live.Load()
	next := slices.Clone(live.workflows)
	for i, w := range live.workflows {
		if !slices.Contains(w.Files(), c.Path) {
			continue
		}
		text, err := os.ReadFile(w.Path)
		if err != nil {
			return http.StatusInternalServerError, fail("%v", err)
		}
		if next[i], err = workflow.ParseWith(w.Path, text, files); err != nil {
			return http.StatusUnprocessableEntity, invalid("Nothing was saved, because "+w.Name+", which uses the component, would no longer be valid.", err)
		}
	}
	if _, err := route(next, s.opts.Env); err != nil {
		return http.StatusUnprocessableEntity, invalid("Nothing was saved, because the server could no longer serve its workflows.", err)
	}
	if err := writeFile(c.Path, out); err != nil {
		return http.StatusInternalServerError, fail("%v", err)
	}
	// The workflows that use the component take up the change like any
	// other change to its file.
	s.refresh()
	s.opts.Log("%s changed from the page: %s", c.Path, strings.Join(changed, ", "))

	components, uses := s.components()
	if fresh := components[id]; fresh != nil {
		return http.StatusOK, map[string]any{"workflow": s.componentView(fresh, uses[id])}
	}
	return http.StatusOK, map[string]any{}
}

// fromGit is the refusal of a change to a component taken from git.
const fromGit = "%s comes from git: change it at its source, then take the new commit with flovr get -u."

// invalid is the answer to a change that would leave a file not valid.
func invalid(message string, err error) failure {
	f := fail("%s", message)
	var problems *workflow.Problems
	if errors.As(err, &problems) {
		for _, p := range problems.Items {
			f.Problems = append(f.Problems, problemView{p.Line, p.Msg})
		}
	} else {
		f.Problems = []problemView{{Message: err.Error()}}
	}
	return f
}

// componentPrompt finds the prompt file of an ai step of a component, or of
// a component that is itself one.
func componentPrompt(c *workflow.Component, step string) (path, written string, ok bool) {
	if c.AI != nil && step == c.Name {
		return c.Resolve(c.AI.Prompt), c.AI.Prompt, true
	}
	for _, st := range c.Steps {
		if st.ID == step && st.AI != nil {
			return c.Resolve(st.AI.Prompt), st.AI.Prompt, true
		}
	}
	return "", "", false
}

// runComponentTest runs one of a component's tests to completion and records
// it as a run, under the name the page knows the component by.
func (s *Server) runComponentTest(id string, c *workflow.Component, t workflow.ComponentTest, name string) Run {
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()

	run := Run{
		ID: id, Workflow: componentID(c), Source: "test", Test: name, Commit: s.commit(),
		Started: time.Now(), Status: running, Steps: []Step{},
	}
	if input, err := c.Values(t); err == nil {
		run.Trigger = map[string]any{"input": input}
	}
	s.history.put(run)
	s.opts.Log("%s %s started by test %q", id, c.Name, name)

	eng := &engine.Engine{Model: s.opts.Model, Env: s.opts.Env}
	res, err := eng.TestComponent(ctx, c, t)
	// A component made of steps shows its steps; another its one kind of work.
	for _, whole := range res.Steps {
		run.Gave = whole.Output
		if len(c.Steps) == 0 {
			run.Steps = append(run.Steps, record(whole))
			continue
		}
		for _, inner := range whole.Steps {
			run.Steps = append(run.Steps, record(inner))
		}
	}
	s.finishTest(&run, res, err)
	s.opts.Log("%s %s test %q %s in %s", id, c.Name, name, run.Status, time.Duration(run.Millis)*time.Millisecond)
	return run
}

// componentCaseNames names the tests of a component as the page lists them.
func componentCaseNames(c *workflow.Component) []string {
	var names []string
	for _, view := range componentCases(c) {
		names = append(names, view.Name)
	}
	return names
}

// stripEdits removes from a description what the page could change.
func stripEdits(view *workflowView) {
	view.Edit = nil
	for i := range view.Steps {
		view.Steps[i].Edit = nil
	}
	if view.Respond != nil {
		view.Respond.Edit = nil
	}
	if view.Gives != nil {
		view.Gives.Edit = nil
	}
}

// sortedIDs returns the names of the components in order.
func sortedIDs(components map[string]*workflow.Component) []string {
	return slices.Sorted(maps.Keys(components))
}

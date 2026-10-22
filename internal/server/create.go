package server

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/flovrlabs/flovr/internal/workflow"
	"github.com/flovrlabs/flovr/internal/yamledit"
)

// createRequest makes a new workflow.
type createRequest struct {
	Name string `json:"name"`
	// Root is the folder to make it in, one of those the page was told of.
	// It can be left out when there is only one.
	Root    string `json:"root"`
	Trigger struct {
		Webhook string `json:"webhook"`
		Cron    string `json:"cron"`
	} `json:"trigger"`
	// Step is the workflow's first step, which uses a component.
	Step struct {
		ID   string            `json:"id"`
		Use  string            `json:"use"`  // the component, by the name the page knows it by
		When string            `json:"when"` // may be empty
		With map[string]string `json:"with"` // what it gives the component, by input
	} `json:"step"`
}

// A workflow made from the page is kept in a folder of its name.
var folderName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

const longestName = 60

// create makes a new workflow: a folder of its name, in one of the folders
// the server was started with, holding a workflow file with a trigger and
// one step. As with every change, nothing is written unless the result is a
// workflow the server can serve along with the others.
func (s *Server) create(req createRequest) (int, any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	refuse := func(format string, args ...any) (int, any) {
		return http.StatusUnprocessableEntity, fail(format, args...)
	}

	roots := s.opts.Roots
	at := slices.IndexFunc(roots, func(root string) bool { return filepath.ToSlash(root) == req.Root })
	switch {
	case len(roots) == 0:
		return refuse("The server was started with workflow files and no folder, so it has nowhere to make a new workflow. " +
			"Start it with a folder, as in: flovr serve workflows --edit")
	case req.Root == "" && len(roots) == 1:
		at = 0
	case at < 0:
		return refuse("A new workflow goes in one of the folders the server was started with: %s.", strings.Join(roots, ", "))
	}

	name := strings.TrimSpace(req.Name)
	live := s.live.Load()
	switch {
	case name == "":
		return refuse("The workflow needs a name.")
	case !folderName.MatchString(name):
		return refuse("The name is also the name of the workflow's folder, so it can hold only letters, digits, - and _, and starts with a letter or a digit.")
	case len(name) > longestName:
		return refuse("The name can be at most %d characters long.", longestName)
	case name == "testdata":
		return refuse("Folders named testdata are passed over when workflows are looked for, so a workflow cannot have that name.")
	case slices.ContainsFunc(live.workflows, func(w *workflow.Workflow) bool { return w.Name == name }):
		return refuse("There is already a workflow named %s.", name)
	}
	dir := filepath.Join(roots[at], name)
	path := filepath.Join(dir, workflow.FileName)
	if _, err := os.Lstat(dir); err == nil {
		return refuse("There is already something at %s.", filepath.ToSlash(dir))
	}

	webhook, schedule := strings.TrimSpace(req.Trigger.Webhook), strings.TrimSpace(req.Trigger.Cron)
	var trigger yamledit.Pair
	switch {
	case (webhook == "") == (schedule == ""):
		return refuse("The workflow needs one thing that starts it: a webhook path or a schedule.")
	case webhook != "":
		trigger = yamledit.Pair{Key: "webhook", Value: webhook}
	default:
		trigger = yamledit.Pair{Key: "cron", Value: schedule}
	}

	known, _ := s.components()
	c := known[req.Step.Use]
	if c == nil {
		return refuse("The first step uses a component, and %q is not one the page knows.", req.Step.Use)
	}
	use := c.Remote
	if use == "" {
		var err error
		if use, err = relative(dir, filepath.Dir(c.Path)); err != nil {
			return refuse("%v", err)
		}
	}
	var inputs []inputView
	for _, p := range c.Input {
		inputs = append(inputs, inputView{p.Name, p.Type})
	}

	var text strings.Builder
	for i, part := range [][]yamledit.Pair{
		{{Key: "flovr", Value: workflow.Format}, {Key: "name", Value: name}},
		{{Key: "trigger", Value: []yamledit.Pair{trigger}}},
		stepUsing(req.Step.ID, req.Step.When, use, req.Step.With, inputs),
	} {
		written, err := yamledit.Mapping(part)
		if err != nil {
			return refuse("%v", err)
		}
		if i == 2 {
			written = "steps:\n" + listItem(written)
		}
		if i > 0 {
			text.WriteString("\n")
		}
		text.WriteString(written)
	}
	data := []byte(text.String())

	fresh, err := workflow.Parse(path, data)
	if err != nil {
		return http.StatusUnprocessableEntity, unwritten("Nothing was made, because the workflow would not be valid.", err)
	}
	next := append(slices.Clone(live.workflows), fresh)
	if _, err := route(next, s.opts.Env); err != nil {
		return http.StatusUnprocessableEntity, unwritten("Nothing was made, because the server could not serve the workflow along with the others.", err)
	}

	// Making the folder fails if something has taken its place since it
	// was looked for, so nothing of anyone's is written over.
	if err := os.Mkdir(dir, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return refuse("There is already something at %s.", filepath.ToSlash(dir))
		}
		return http.StatusInternalServerError, fail("%v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		os.Remove(path)
		os.Remove(dir)
		return http.StatusInternalServerError, fail("%v", err)
	}
	s.sources[path] = sourceOf(path, data, fresh)
	s.serve(next)
	s.opts.Log("%s made from the page: %s", name, path)
	return http.StatusOK, map[string]any{"workflow": s.view(fresh)}
}

// unwritten is the answer to a request for a file that would not be valid.
// The file was never written, so the problems go without their lines.
func unwritten(message string, err error) failure {
	f := invalid(message, err)
	for i := range f.Problems {
		f.Problems[i].Line = 0
	}
	return f
}

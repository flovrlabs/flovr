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
	"time"

	"github.com/flovrlabs/flovr/internal/workflow"
	"github.com/flovrlabs/flovr/internal/yamledit"
)

// componentRequest makes a new component, which the page calls a service: a
// folder of its name holding a component file that does one piece of work.
type componentRequest struct {
	Name string `json:"name"`
	// Root is the folder to make it in, one of those the page was told of.
	// It can be left out when there is only one.
	Root        string `json:"root"`
	Description string `json:"description"`
	Inputs      []struct {
		Name string `json:"name"`
		Type string `json:"type"` // one of workflow.ParamTypes
	} `json:"inputs"`
	// Work is what it does, as for a new step that does its own work: its
	// Kind is "ai", "decide" or "http". Its id and condition are not used.
	Work stepAdd `json:"work"`
}

// componentsDir is the folder, inside a root, that components made from the
// page go in.
const componentsDir = "components"

// An input is read inside the component as input.NAME.
var inputName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// createComponent makes a new component in the components folder of one of
// the folders the server was started with. Nothing is kept unless the result
// is a component that loads.
func (s *Server) createComponent(req componentRequest) (int, any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	refuse := func(format string, args ...any) (int, any) {
		return http.StatusUnprocessableEntity, fail(format, args...)
	}

	roots := s.opts.Roots
	at := slices.IndexFunc(roots, func(root string) bool { return filepath.ToSlash(root) == req.Root })
	switch {
	case len(roots) == 0:
		return refuse("The server was started with workflow files and no folder, so it has nowhere to make a new service. " +
			"Start it with a folder, as in: flovr serve workflows --edit")
	case req.Root == "" && len(roots) == 1:
		at = 0
	case at < 0:
		return refuse("A new service goes in one of the folders the server was started with: %s.", strings.Join(roots, ", "))
	}

	name := strings.TrimSpace(req.Name)
	switch {
	case name == "":
		return refuse("The service needs a name.")
	case !folderName.MatchString(name):
		return refuse("The name is also the name of the service's folder, so it can hold only letters, digits, - and _, and starts with a letter or a digit.")
	case len(name) > longestName:
		return refuse("The name can be at most %d characters long.", longestName)
	case name == "testdata":
		return refuse("Folders named testdata are passed over when services are looked for, so a service cannot have that name.")
	}
	parent := filepath.Join(roots[at], componentsDir)
	dir := filepath.Join(parent, name)
	path := filepath.Join(dir, workflow.ComponentFile)
	if _, err := os.Lstat(dir); err == nil {
		return refuse("There is already something at %s.", filepath.ToSlash(dir))
	}

	var input []yamledit.Pair
	for _, in := range req.Inputs {
		n, t := strings.TrimSpace(in.Name), strings.TrimSpace(in.Type)
		switch {
		case !inputName.MatchString(n):
			return refuse("%q cannot name an input: a name holds letters, digits and _, and starts with a letter or _.", n)
		case slices.ContainsFunc(input, func(p yamledit.Pair) bool { return p.Key == n }):
			return refuse("Two inputs are called %s.", n)
		case !slices.Contains(workflow.ParamTypes, t):
			return refuse("The input %s is of the type %q. A type is one of %s.", n, t, strings.Join(workflow.ParamTypes, ", "))
		}
		input = append(input, yamledit.Pair{Key: n, Value: t})
	}
	if !slices.Contains([]string{"ai", "decide", "http"}, req.Work.Kind) {
		return refuse("A service does one piece of work: it asks a model to write or to decide, or sends an HTTP request.")
	}

	// The folder is made first, because the prompt and schema of an ai
	// service are written into it. Making it fails if something has taken
	// its place since it was looked for, so nothing of anyone's is written
	// over; whatever goes wrong after, it is taken away again.
	_, statErr := os.Lstat(parent)
	newParent := errors.Is(statErr, fs.ErrNotExist)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return http.StatusInternalServerError, fail("%v", err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return refuse("There is already something at %s.", filepath.ToSlash(dir))
		}
		return http.StatusInternalServerError, fail("%v", err)
	}
	undo := func() {
		os.RemoveAll(dir)
		if newParent {
			os.Remove(parent)
		}
	}

	work := req.Work
	work.ID, work.When = name, ""
	pairs, _, status, refused := ownWork(dir, &work)
	if status != 0 {
		undo()
		return status, refused
	}
	head := []yamledit.Pair{{Key: "name", Value: name}}
	if d := strings.TrimSpace(req.Description); d != "" {
		head = append(head, yamledit.Pair{Key: "description", Value: d})
	}
	parts := [][]yamledit.Pair{head}
	if len(input) > 0 {
		parts = append(parts, []yamledit.Pair{{Key: "input", Value: input}})
	}
	// The work itself, without the id that a step has.
	parts = append(parts, pairs[len(pairs)-1:])
	var text strings.Builder
	for i, part := range parts {
		written, err := yamledit.Mapping(part)
		if err != nil {
			undo()
			return refuse("%v", err)
		}
		if i > 0 {
			text.WriteString("\n")
		}
		text.WriteString(written)
	}
	data := []byte(text.String())

	if _, err := workflow.LoadComponentWith(path, workflow.Overlay{path: data}); err != nil {
		undo()
		return http.StatusUnprocessableEntity, unwritten("Nothing was made, because the service would not be valid.", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		undo()
		return http.StatusInternalServerError, fail("%v", err)
	}
	c, err := workflow.LoadComponent(path)
	if err != nil {
		undo()
		return http.StatusInternalServerError, fail("%v", err)
	}
	// The folders are looked through again at once, so the page lists it.
	s.found.Lock()
	s.found.at = time.Time{}
	s.found.Unlock()
	s.opts.Log("service %s made from the page: %s", name, path)
	return http.StatusOK, map[string]any{"component": s.componentView(c, nil)}
}

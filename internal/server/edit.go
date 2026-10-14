package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/simonyosb/flowmaker/internal/workflow"
	"github.com/simonyosb/flowmaker/internal/yamledit"
)

// source is what the server knows of a workflow file the page may change.
type source struct {
	rev     string // identifies the content of the workflow file being served
	built   string // identifies that and the content of the component files
	files   []seen // the workflow file and the component files, as last read
	problem string // why what the files hold now is not being served
}

// seen is a file as it was when last looked at.
type seen struct {
	path string
	mod  time.Time
	size int64
}

func look(path string) seen {
	info, err := os.Stat(path)
	if err != nil {
		return seen{path: path, size: -1}
	}
	return seen{path, info.ModTime(), info.Size()}
}

// unchanged reports whether none of the files looks different from when it
// was last read.
func (s *source) unchanged() bool {
	for _, f := range s.files {
		if now := look(f.path); now.size != f.size || !now.mod.Equal(f.mod) {
			return false
		}
	}
	return true
}

// read loads the workflow file at path. It returns the source even if the
// file was read but is not a valid workflow.
func read(path string) (*workflow.Workflow, *source, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	w, err := workflow.Parse(path, data)
	return w, sourceOf(path, data, w), err
}

// sourceOf describes the workflow file at path, which holds data, and the
// component files that w, if it is not nil, was built from.
func sourceOf(path string, data []byte, w *workflow.Workflow) *source {
	src := &source{rev: revision(data), files: []seen{look(path)}}
	all := sha256.New()
	all.Write(data)
	if w != nil {
		for _, file := range w.Files() {
			src.files = append(src.files, look(file))
			content, _ := os.ReadFile(file)
			fmt.Fprintf(all, "\x00%s\x00", file)
			all.Write(content)
		}
	}
	src.built = hex.EncodeToString(all.Sum(nil)[:8])
	return src
}

func revision(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// refresh takes up changes made to the workflow files and the component
// files they use since they were read, by an editor or by git. A workflow
// whose files are no longer valid keeps being served as it was, and the
// reason is kept for the page to show. The caller holds s.mu.
func (s *Server) refresh() {
	workflows := slices.Clone(s.live.Load().workflows)
	changed := false
	for i, w := range workflows {
		src := s.sources[w.Path]
		// What made the files invalid may lie in a file not yet known of, so
		// while there is a problem they are read every time.
		if src.problem == "" && src.unchanged() {
			continue
		}
		fresh, now, err := read(w.Path)
		if now == nil {
			src.problem = "the file cannot be read: " + err.Error()
			continue
		}
		if err == nil && now.built == src.built {
			src.files, src.problem = now.files, ""
			continue
		}
		if err == nil {
			next := slices.Clone(workflows)
			next[i] = fresh
			_, err = route(next, s.opts.Env)
		}
		if err != nil {
			// The times are kept, so the files are not read again for
			// nothing once the problem is gone.
			src.problem = err.Error()
			continue
		}
		workflows[i] = fresh
		*src = *now
		changed = true
		s.opts.Log("%s reloaded from %s", fresh.Name, fresh.Path)
	}
	if changed {
		s.serve(workflows)
	}
}

// serve replaces the workflows being served. They have passed route. The
// caller holds s.mu.
func (s *Server) serve(workflows []*workflow.Workflow) {
	r, err := route(workflows, s.opts.Env)
	if err != nil {
		return
	}
	s.live.Store(r)
	s.schedule()
	s.version.Lock()
	s.version.asked = time.Time{}
	s.version.Unlock()
}

// failure is the answer to a change that was not made.
type failure struct {
	Error    string        `json:"error"`
	Problems []problemView `json:"problems,omitempty"`
}

type problemView struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

func fail(format string, args ...any) failure {
	return failure{Error: fmt.Sprintf(format, args...)}
}

const stale = "The file changed after the page read it, so nothing was saved. The page now shows the file as it is."

// change handles the requests that write files.
func (s *Server) change(rw http.ResponseWriter, r *http.Request) {
	if !s.mayView(r) {
		reply(rw, http.StatusUnauthorized, fail("missing or wrong token"))
		return
	}
	// A page on another site cannot send JSON here without asking first, and
	// a browser says where a request comes from. Together they keep such a
	// page from changing files.
	if kind, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); kind != "application/json" {
		reply(rw, http.StatusUnsupportedMediaType, fail("send the body with Content-Type: application/json"))
		return
	}
	if !sameOrigin(r) {
		reply(rw, http.StatusForbidden, fail("changes are accepted only from the server's own page"))
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, maxBody))
	if err != nil {
		reply(rw, http.StatusRequestEntityTooLarge, fail("the request body is larger than 1 MB"))
		return
	}

	var status int
	var answer any
	if r.URL.Path == "/api/edit" {
		var req editRequest
		if err := json.Unmarshal(data, &req); err != nil {
			reply(rw, http.StatusBadRequest, fail("the request body is not the JSON expected"))
			return
		}
		status, answer = s.edit(req)
	} else if r.URL.Path == "/api/workflows" {
		var req createRequest
		if err := json.Unmarshal(data, &req); err != nil {
			reply(rw, http.StatusBadRequest, fail("the request body is not the JSON expected"))
			return
		}
		status, answer = s.create(req)
	} else if r.URL.Path == "/api/cases" {
		var req casesRequest
		if err := json.Unmarshal(data, &req); err != nil {
			reply(rw, http.StatusBadRequest, fail("the request body is not the JSON expected"))
			return
		}
		status, answer = s.cases(req)
	} else if r.URL.Path == "/api/test" {
		var req testRequest
		if err := json.Unmarshal(data, &req); err != nil {
			reply(rw, http.StatusBadRequest, fail("the request body is not the JSON expected"))
			return
		}
		status, answer = s.test(req)
	} else if r.URL.Path == "/api/steps" {
		var req stepsRequest
		if err := json.Unmarshal(data, &req); err != nil || (req.Add == nil) == (req.Remove == "") {
			reply(rw, http.StatusBadRequest, fail("the request body is not the JSON expected: it adds a step or removes one"))
			return
		}
		status, answer = s.steps(req)
	} else {
		var req promptRequest
		if err := json.Unmarshal(data, &req); err != nil {
			reply(rw, http.StatusBadRequest, fail("the request body is not the JSON expected"))
			return
		}
		status, answer = s.savePrompt(req)
	}
	reply(rw, status, answer)
}

// sameOrigin reports whether a request comes from the server's own page, or
// from a program that is not a browser.
func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}

type editRequest struct {
	Workflow string `json:"workflow"`
	Rev      string `json:"rev"` // the file as the page last saw it
	Edits    []struct {
		Path  []any  `json:"path"`
		Value string `json:"value"`
		// Node, if given, is what the page takes the value to belong to: a
		// step's id, "trigger" or "response". Steps are found by position,
		// and a position holds another step once steps are added or removed.
		Node string `json:"node"`
	} `json:"edits"`
}

// open finds the workflow a change is for and reads its file. It refuses, with
// a status and the reason, if the file is not what the page read. The caller
// holds s.mu.
func (s *Server) open(name, rev string) (i int, w *workflow.Workflow, data []byte, status int, f failure) {
	s.refresh()
	live := s.live.Load()
	i = slices.IndexFunc(live.workflows, func(w *workflow.Workflow) bool { return w.Name == name })
	if i < 0 {
		return 0, nil, nil, http.StatusNotFound, fail("no workflow is named %s", name)
	}
	w = live.workflows[i]
	src := s.sources[w.Path]
	data, err := os.ReadFile(w.Path)
	switch {
	case err != nil:
		return 0, nil, nil, http.StatusInternalServerError, fail("%v", err)
	case src.problem != "":
		return 0, nil, nil, http.StatusConflict, fail("%s has a problem that must be fixed in the file before the page can change it: %s", w.Path, src.problem)
	case revision(data) != src.rev:
		// The file changed without its time or size showing it, so it is
		// read again whatever they say.
		src.files[0].size = -1
		return 0, nil, nil, http.StatusConflict, fail(stale)
	case rev != src.rev:
		return 0, nil, nil, http.StatusConflict, fail(stale)
	}
	return i, w, data, 0, failure{}
}

// save replaces the content of workflow i's file with out, if out is a
// workflow the server can go on serving, and serves it. what says what was
// changed, for the log. The caller holds s.mu.
func (s *Server) save(i int, out []byte, what string) (int, any) {
	live := s.live.Load()
	w := live.workflows[i]
	fresh, err := workflow.Parse(w.Path, out)
	if err == nil {
		next := slices.Clone(live.workflows)
		next[i] = fresh
		if _, err = route(next, s.opts.Env); err == nil {
			if err := writeFile(w.Path, out); err != nil {
				return http.StatusInternalServerError, fail("%v", err)
			}
			*s.sources[w.Path] = *sourceOf(w.Path, out, fresh)
			s.serve(next)
			s.opts.Log("%s changed from the page: %s", fresh.Name, what)
			return http.StatusOK, map[string]any{"workflow": s.view(fresh)}
		}
	}

	f := fail("Nothing was saved, because the workflow would no longer be valid.")
	var problems *workflow.Problems
	if errors.As(err, &problems) {
		for _, p := range problems.Items {
			f.Problems = append(f.Problems, problemView{p.Line, p.Msg})
		}
	} else {
		f.Problems = []problemView{{Message: err.Error()}}
	}
	return http.StatusUnprocessableEntity, f
}

// edit changes values in a workflow file. The file is written only if the
// result is a workflow the server can go on serving.
func (s *Server) edit(req editRequest) (int, any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.componentNamed(req.Workflow); c != nil {
		return s.editComponent(c, req)
	}
	i, w, data, status, refused := s.open(req.Workflow, req.Rev)
	if status != 0 {
		return status, refused
	}

	edits, changed, status, refused := editsFor(describe(w), func(path []any) string { return owner(w, path) }, req)
	if status != 0 {
		return status, refused
	}

	out, err := yamledit.Apply(data, edits...)
	if err != nil {
		return http.StatusUnprocessableEntity, fail("%v", err)
	}
	if bytes.Equal(out, data) {
		return http.StatusOK, map[string]any{"workflow": s.view(w)}
	}
	return s.save(i, out, strings.Join(changed, ", "))
}

// editsFor turns the changes a request asks for into edits of the file that
// view describes. It refuses, with a status and the reason, a change to a
// value the page was not offered or was offered for another step. owner
// names what the value at a path belongs to.
func editsFor(view workflowView, owner func(path []any) string, req editRequest) (edits []yamledit.Edit, changed []string, status int, refused failure) {
	offered := map[string]fieldView{}
	for _, f := range view.Edit {
		offered[pathKey(f.Path)] = f
	}
	for _, step := range view.Steps {
		for _, f := range step.Edit {
			offered[pathKey(f.Path)] = f
		}
	}
	if view.Respond != nil {
		for _, f := range view.Respond.Edit {
			offered[pathKey(f.Path)] = f
		}
	}
	if view.Gives != nil {
		for _, f := range view.Gives.Edit {
			offered[pathKey(f.Path)] = f
		}
	}

	for _, e := range req.Edits {
		path := wholeNumbers(e.Path)
		f, ok := offered[pathKey(path)]
		value := strings.TrimSpace(e.Value)
		switch {
		case !ok || f.Kind == kindPrompt:
			return nil, nil, http.StatusUnprocessableEntity, fail("the page cannot change this value")
		case e.Node != "" && e.Node != owner(path):
			return nil, nil, http.StatusConflict, fail("The steps of the file changed after the page read it, so nothing was saved. The page now shows the file as it is.")
		case f.Kind == kindChoice && !slices.Contains(f.Options, value):
			return nil, nil, http.StatusUnprocessableEntity, fail("%s must be one of %s", f.Label, strings.Join(f.Options, ", "))
		}
		edit := yamledit.Edit{Path: path, Value: value, Remove: value == "" && f.Optional}
		if f.Kind == kindNumber && !edit.Remove {
			// A number is written as one, not as text that looks like one.
			n, err := strconv.Atoi(value)
			if err != nil {
				return nil, nil, http.StatusUnprocessableEntity, fail("%s must be a whole number", f.Label)
			}
			edit.Value = n
		}
		// Where a key that is new to the file goes.
		switch last := path[len(path)-1]; {
		case last == "when":
			edit.After = "id"
		case pathKey(path) == "trigger.input":
			edit.After = "webhook"
		}
		edits = append(edits, edit)
		changed = append(changed, pathKey(path))
	}
	return edits, changed, 0, failure{}
}

// owner names what the value at path belongs to, as the page names it: a
// step's id, "trigger" or "response".
func owner(w *workflow.Workflow, path []any) string {
	switch path[0] {
	case "trigger":
		return "trigger"
	case "respond":
		return "response"
	case "steps":
		if i, ok := path[1].(int); len(path) > 1 && ok && i >= 0 && i < len(w.Steps) {
			return w.Steps[i].ID
		}
	}
	return ""
}

// stepsRequest adds a step to a workflow, or removes one.
type stepsRequest struct {
	Workflow string `json:"workflow"`
	Rev      string `json:"rev"` // the file as the page last saw it
	Add      *struct {
		ID string `json:"id"`
		// After is the step the new one comes after, "trigger" to make it
		// the first step, or empty to make it the last.
		After string            `json:"after"`
		Use   string            `json:"use"`  // the component it uses, as the workflow's list names it
		When  string            `json:"when"` // may be empty
		With  map[string]string `json:"with"` // what it gives the component, by input
	} `json:"add"`
	Remove string `json:"remove"` // the id of the step to remove
}

// steps adds a step that uses a component to a workflow file, or removes a
// step from it. As with edit, the file is written only if the result is a
// workflow the server can go on serving.
func (s *Server) steps(req stepsRequest) (int, any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.componentNamed(req.Workflow) != nil {
		return http.StatusUnprocessableEntity, fail("The steps of a component are added and removed in its file.")
	}
	i, w, data, status, refused := s.open(req.Workflow, req.Rev)
	if status != 0 {
		return status, refused
	}
	position := func(id string) int {
		return slices.IndexFunc(w.Steps, func(st workflow.Step) bool { return st.ID == id })
	}

	if add := req.Add; add != nil {
		at := len(w.Steps)
		switch {
		case add.After == "trigger":
			at = 0
		case add.After != "":
			if at = position(add.After) + 1; at == 0 {
				return http.StatusUnprocessableEntity, fail("there is no step %s to put the new step after", add.After)
			}
		}
		// The page adds steps only from the components it was shown.
		known, _ := s.components()
		components := catalogue(w, known)
		c := slices.IndexFunc(components, func(c componentView) bool { return c.Use == add.Use })
		if c < 0 {
			return http.StatusUnprocessableEntity, fail("%s is not one of the components this workflow can use", add.Use)
		}
		text, err := yamledit.Mapping(stepUsing(add.ID, add.When, add.Use, add.With, components[c].Inputs))
		if err != nil {
			return http.StatusUnprocessableEntity, fail("%v", err)
		}
		out, err := yamledit.InsertItem(data, []any{"steps"}, at, text)
		if err != nil {
			return http.StatusUnprocessableEntity, fail("%v", err)
		}
		return s.save(i, out, fmt.Sprintf("step %s added, using %s", add.ID, add.Use))
	}

	at := position(req.Remove)
	if at < 0 {
		return http.StatusUnprocessableEntity, fail("there is no step %s to remove", req.Remove)
	}
	out, err := yamledit.RemoveItem(data, []any{"steps"}, at)
	if err != nil {
		return http.StatusUnprocessableEntity, fail("%v", err)
	}
	return s.save(i, out, fmt.Sprintf("step %s removed", req.Remove))
}

// stepUsing is a step that uses a component, as it is written in a file: its
// id, the condition if it has one, the component as the file names it, and
// what it gives the component, in the order the component lists its input.
func stepUsing(id, when, use string, with map[string]string, inputs []inputView) []yamledit.Pair {
	pairs := []yamledit.Pair{{Key: "id", Value: strings.TrimSpace(id)}}
	if when = strings.TrimSpace(when); when != "" {
		pairs = append(pairs, yamledit.Pair{Key: "when", Value: when})
	}
	pairs = append(pairs, yamledit.Pair{Key: "use", Value: use})
	var given []yamledit.Pair
	for _, input := range inputs {
		if text, ok := with[input.Name]; ok {
			given = append(given, yamledit.Pair{Key: input.Name, Value: written(strings.TrimSpace(text), input.Type)})
		}
	}
	return append(pairs, yamledit.Pair{Key: "with", Value: given})
}

// written is how a value typed into the page is put in the file for an input
// of the given type: a number or a yes/no as such, anything else as text,
// which may be an expression.
func written(text, kind string) any {
	switch kind {
	case "number":
		if n, err := strconv.Atoi(text); err == nil {
			return n
		}
		if f, err := strconv.ParseFloat(text, 64); err == nil {
			return f
		}
	case "boolean":
		if b, err := strconv.ParseBool(text); err == nil && (text == "true" || text == "false") {
			return b
		}
	}
	return text
}

// componentView is a component that a step can be added from.
type componentView struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Use         string      `json:"use"`  // how a step of the workflow names it
	Kind        string      `json:"kind"` // what it does: "ai", "decide" or "http"
	Inputs      []inputView `json:"inputs"`
}

type inputView struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// catalogue lists the components a step of w can be added from: those the
// page knows, each named as a step of w would name it. The ones in the
// workflow's own folder come first.
func catalogue(w *workflow.Workflow, known map[string]*workflow.Component) []componentView {
	dir := filepath.Dir(w.Path)
	views := []componentView{}
	for _, id := range sortedIDs(known) {
		c := known[id]
		use, err := relative(dir, filepath.Dir(c.Path))
		if err != nil {
			continue
		}
		view := componentView{
			Name: c.Name, Description: c.Description, Use: use, Kind: c.Kind(),
			Inputs: []inputView{},
		}
		for _, p := range c.Input {
			view.Inputs = append(view.Inputs, inputView{p.Name, p.Type})
		}
		views = append(views, view)
	}
	elsewhere := func(v componentView) bool { return strings.HasPrefix(v.Use, "../") }
	slices.SortStableFunc(views, func(a, b componentView) int {
		switch {
		case elsewhere(a) == elsewhere(b):
			return strings.Compare(a.Use, b.Use)
		case elsewhere(a):
			return 1
		}
		return -1
	})
	return views
}

// relative is the path of to as a file in the folder from names it, with
// forward slashes.
func relative(from, to string) (string, error) {
	from, err := filepath.Abs(from)
	if err != nil {
		return "", err
	}
	if to, err = filepath.Abs(to); err != nil {
		return "", err
	}
	rel, err := filepath.Rel(from, to)
	return filepath.ToSlash(rel), err
}

// below reports whether the folder at dir is root or inside it, going by
// their names.
func below(root, dir string) bool {
	rel, err := relative(root, dir)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

// pathKey writes a path as text, to compare paths and to name them in the log.
func pathKey(path []any) string {
	var sb strings.Builder
	for _, p := range path {
		if n, ok := p.(int); ok {
			fmt.Fprintf(&sb, "[%d]", n)
			continue
		}
		if sb.Len() > 0 {
			sb.WriteByte('.')
		}
		fmt.Fprint(&sb, p)
	}
	return sb.String()
}

// wholeNumbers turns the numbers of a path decoded from JSON into list
// positions.
func wholeNumbers(path []any) []any {
	out := make([]any, len(path))
	for i, p := range path {
		if f, ok := p.(float64); ok && f == float64(int(f)) {
			p = int(f)
		}
		out[i] = p
	}
	return out
}

// writeFile replaces the content of the file at path in one step, so nothing
// ever reads half of it.
func writeFile(path string, data []byte) error {
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".flowmaker-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Chmod(info.Mode().Perm())
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// promptView is the prompt file of an ai step.
type promptView struct {
	File string `json:"file"` // as the workflow names it
	Path string `json:"path"` // where it is, as the workflow's own path is given
	Text string `json:"text"`
	Rev  string `json:"rev"`
}

type promptRequest struct {
	Workflow string `json:"workflow"`
	Step     string `json:"step"`
	Rev      string `json:"rev"` // the file as the page last saw it
	Text     string `json:"text"`
}

// promptFile finds the prompt file of an ai step. The page reads and writes
// such files only inside the workflow's own folder.
func (s *Server) promptFile(name, step string) (path, written string, status int, err error) {
	for _, w := range s.live.Load().workflows {
		if w.Name != name {
			continue
		}
		for _, st := range w.Steps {
			if st.ID != step || st.AI == nil {
				continue
			}
			path := w.Resolve(st.AI.Prompt)
			if !within(filepath.Dir(w.Path), path) {
				return "", "", http.StatusForbidden, fmt.Errorf("%s is outside the workflow's folder, so the page does not open it", st.AI.Prompt)
			}
			return path, st.AI.Prompt, 0, nil
		}
	}
	if c := s.componentNamed(name); c != nil {
		if path, written, ok := componentPrompt(c, step); ok {
			if !within(filepath.Dir(c.Path), path) {
				return "", "", http.StatusForbidden, fmt.Errorf("%s is outside the component's folder, so the page does not open it", written)
			}
			return path, written, 0, nil
		}
	}
	return "", "", http.StatusNotFound, fmt.Errorf("no ai step %s in a workflow named %s", step, name)
}

// componentNamed returns the component the page knows by name, or nil if
// the name is a workflow's or nobody's.
func (s *Server) componentNamed(name string) *workflow.Component {
	components, _ := s.components()
	return components[name]
}

// within reports whether the file at path is inside dir, wherever links lead.
func within(dir, path string) bool {
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	if path, err = filepath.EvalSymlinks(path); err != nil {
		return false
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return false
	}
	if path, err = filepath.Abs(path); err != nil {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && filepath.IsLocal(rel)
}

func (s *Server) prompt(rw http.ResponseWriter, r *http.Request) {
	path, written, status, err := s.promptFile(r.URL.Query().Get("workflow"), r.URL.Query().Get("step"))
	if err != nil {
		reply(rw, status, fail("%v", err))
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		reply(rw, http.StatusInternalServerError, fail("%v", err))
		return
	}
	reply(rw, http.StatusOK, promptView{File: written, Path: path, Text: string(data), Rev: revision(data)})
}

func (s *Server) savePrompt(req promptRequest) (int, any) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path, written, status, err := s.promptFile(req.Workflow, req.Step)
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
	// A text file ends with one line break, whatever the page sent.
	text := strings.TrimRight(strings.ReplaceAll(req.Text, "\r\n", "\n"), " \t\n")
	if text == "" {
		return http.StatusUnprocessableEntity, fail("Nothing was saved, because a prompt cannot be empty.")
	}
	out := []byte(text + "\n")
	if !bytes.Equal(out, data) {
		if err := writeFile(path, out); err != nil {
			return http.StatusInternalServerError, fail("%v", err)
		}
		s.version.Lock()
		s.version.asked = time.Time{}
		s.version.Unlock()
		s.opts.Log("%s changed from the page: the prompt of %s (%s)", req.Workflow, req.Step, written)
	}
	return http.StatusOK, promptView{File: written, Path: path, Text: string(out), Rev: revision(out)}
}

// changesView is what git says has changed in a workflow's folder since the
// last commit.
type changesView struct {
	Git   bool         `json:"git"` // false if the folder is not in a git repository
	Files []changeView `json:"files"`
	Diff  string       `json:"diff"`
}

type changeView struct {
	Path   string `json:"path"`   // from the top of the repository
	Change string `json:"change"` // "modified", "new", "deleted" or "renamed"
}

const maxDiff = 256 << 10

func (s *Server) changes(rw http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("workflow")
	live := s.live.Load()
	i := slices.IndexFunc(live.workflows, func(w *workflow.Workflow) bool { return w.Name == name })
	if i < 0 {
		reply(rw, http.StatusNotFound, fail("no workflow is named %s", name))
		return
	}
	dir := filepath.Dir(live.workflows[i].Path)
	view := changesView{Files: []changeView{}}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	// Every file git does not know is listed, not just the folder it is in.
	status, err := git(ctx, dir, "status", "--porcelain", "--untracked-files=all", "--", ".")
	if err != nil {
		reply(rw, http.StatusOK, view)
		return
	}
	view.Git = true
	var unknown []string
	for _, line := range strings.Split(status, "\n") {
		if len(line) < 4 {
			continue
		}
		code, path := line[:2], line[3:]
		change := "modified"
		switch {
		case code == "??":
			change = "new"
			unknown = append(unknown, path)
		case strings.Contains(code, "A"):
			change = "new"
		case strings.Contains(code, "D"):
			change = "deleted"
		case strings.Contains(code, "R"):
			change = "renamed"
		}
		view.Files = append(view.Files, changeView{Path: path, Change: change})
	}
	// A repository without a commit has nothing to compare with.
	diff, _ := git(ctx, dir, "diff", "--no-color", "--no-ext-diff", "HEAD", "--", ".")
	// A file git does not know is in no diff of its own, so it is shown as
	// one that is all new. Its path is from the top of the repository.
	if top, err := git(ctx, dir, "rev-parse", "--show-toplevel"); err == nil {
		for _, path := range unknown {
			if len(diff) > maxDiff || strings.HasPrefix(path, `"`) {
				break
			}
			diff += added(ctx, strings.TrimSpace(top), path)
		}
	}
	if len(diff) > maxDiff {
		diff = diff[:maxDiff] + "\n… the rest is not shown\n"
	}
	view.Diff = diff
	reply(rw, http.StatusOK, view)
}

// added is the diff git gives for the file at path in dir when there was
// nothing before it.
func added(ctx context.Context, dir, path string) string {
	out, err := git(ctx, dir, "diff", "--no-color", "--no-ext-diff", "--no-index", "--", os.DevNull, path)
	// Comparing two files that differ ends with status 1.
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
		return ""
	}
	return out
}

// git runs a git command that only reads, in dir.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	// Reading must not get in the way of a git command the person is running.
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	return string(out), err
}

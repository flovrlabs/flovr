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
	"strings"
	"time"

	"aia/internal/workflow"
	"aia/internal/yamledit"
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
	} `json:"edits"`
}

// edit changes values in a workflow file. The file is written only if the
// result is a workflow the server can go on serving.
func (s *Server) edit(req editRequest) (int, any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()

	live := s.live.Load()
	i := slices.IndexFunc(live.workflows, func(w *workflow.Workflow) bool { return w.Name == req.Workflow })
	if i < 0 {
		return http.StatusNotFound, fail("no workflow is named %s", req.Workflow)
	}
	w := live.workflows[i]
	src := s.sources[w.Path]
	data, err := os.ReadFile(w.Path)
	switch {
	case err != nil:
		return http.StatusInternalServerError, fail("%v", err)
	case src.problem != "":
		return http.StatusConflict, fail("%s has a problem that must be fixed in the file before the page can change it: %s", w.Path, src.problem)
	case revision(data) != src.rev:
		// The file changed without its time or size showing it, so it is
		// read again whatever they say.
		src.files[0].size = -1
		return http.StatusConflict, fail(stale)
	case req.Rev != src.rev:
		return http.StatusConflict, fail(stale)
	}

	view := describe(w)
	offered := map[string]fieldView{}
	for _, f := range view.Edit {
		offered[pathKey(f.Path)] = f
	}
	for _, step := range view.Steps {
		for _, f := range step.Edit {
			offered[pathKey(f.Path)] = f
		}
	}

	var edits []yamledit.Edit
	var changed []string
	for _, e := range req.Edits {
		path := wholeNumbers(e.Path)
		f, ok := offered[pathKey(path)]
		value := strings.TrimSpace(e.Value)
		switch {
		case !ok || f.Kind == kindPrompt:
			return http.StatusUnprocessableEntity, fail("the page cannot change this value")
		case f.Kind == kindChoice && !slices.Contains(f.Options, value):
			return http.StatusUnprocessableEntity, fail("%s must be one of %s", f.Label, strings.Join(f.Options, ", "))
		}
		edit := yamledit.Edit{Path: path, Value: value, Remove: value == "" && f.Optional}
		if path[len(path)-1] == "when" {
			edit.After = "id"
		}
		edits = append(edits, edit)
		changed = append(changed, pathKey(path))
	}

	out, err := yamledit.Apply(data, edits...)
	if err != nil {
		return http.StatusUnprocessableEntity, fail("%v", err)
	}
	if bytes.Equal(out, data) {
		return http.StatusOK, map[string]any{"workflow": s.view(w)}
	}
	fresh, err := workflow.Parse(w.Path, out)
	if err == nil {
		next := slices.Clone(live.workflows)
		next[i] = fresh
		if _, err = route(next, s.opts.Env); err == nil {
			err = writeFile(w.Path, out)
			if err != nil {
				return http.StatusInternalServerError, fail("%v", err)
			}
			*src = *sourceOf(w.Path, out, fresh)
			s.serve(next)
			s.opts.Log("%s changed from the page: %s", fresh.Name, strings.Join(changed, ", "))
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
	tmp, err := os.CreateTemp(filepath.Dir(path), ".aia-*")
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
	return "", "", http.StatusNotFound, fmt.Errorf("no ai step %s in a workflow named %s", step, name)
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
	status, err := git(ctx, dir, "status", "--porcelain", "--", ".")
	if err != nil {
		reply(rw, http.StatusOK, view)
		return
	}
	view.Git = true
	for _, line := range strings.Split(status, "\n") {
		if len(line) < 4 {
			continue
		}
		code, path := line[:2], line[3:]
		change := "modified"
		switch {
		case code == "??" || strings.Contains(code, "A"):
			change = "new"
		case strings.Contains(code, "D"):
			change = "deleted"
		case strings.Contains(code, "R"):
			change = "renamed"
		}
		view.Files = append(view.Files, changeView{Path: path, Change: change})
	}
	// A repository without a commit has nothing to compare with.
	if diff, err := git(ctx, dir, "diff", "--no-color", "--no-ext-diff", "HEAD", "--", "."); err == nil {
		if len(diff) > maxDiff {
			diff = diff[:maxDiff] + "\n… the rest is not shown\n"
		}
		view.Diff = diff
	}
	reply(rw, http.StatusOK, view)
}

// git runs a git command that only reads, in dir.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	// Reading must not get in the way of a git command the person is running.
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	return string(out), err
}

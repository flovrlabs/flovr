package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/flovrlabs/flovr/internal/fetch"
	"github.com/flovrlabs/flovr/internal/workflow"
)

// The market is a list of components kept in git that anyone may use, read
// from where Options.Market says: an https address, or a file on this
// machine. The page lists them beside the project's own, and a step added
// from one takes it from git and pins it in the lock file, as flovr get
// does. What it lists is only offered: what a component is, is what its
// repository holds, and that is checked when it is taken.

// marketEntry is a component of the market, as the list describes it.
type marketEntry struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Use         string      `json:"use"`  // as a step names it, with the tag to take
	Kind        string      `json:"kind"` // "ai", "decide", "http" or "flow"
	Inputs      []inputView `json:"inputs"`
	Tags        []string    `json:"tags,omitempty"`
}

// marketView is the market for the page.
type marketView struct {
	Source   string        `json:"source"`
	Services []marketEntry `json:"services"`
	Skipped  int           `json:"skipped,omitempty"` // entries left out, as not valid
	Error    string        `json:"error,omitempty"`
}

const (
	marketMax   = 1 << 20         // the most of a list that is read
	marketEvery = 5 * time.Minute // how long a list read is kept
)

var tagRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,29}$`)

// market returns the market, read again once what was read is old, or soon
// after it could not be read.
func (s *Server) market(ctx context.Context) marketView {
	m := &s.marketRead
	m.Lock()
	defer m.Unlock()
	stale := time.Since(m.at) > marketEvery || (m.view.Error != "" && time.Since(m.at) > 10*time.Second)
	if m.at.IsZero() || stale {
		m.view = readMarket(ctx, s.opts.Market)
		m.at = time.Now()
	}
	return m.view
}

func readMarket(ctx context.Context, source string) marketView {
	view := marketView{Source: source, Services: []marketEntry{}}
	if source == "" {
		view.Error = "No market is set: start the server with --market and the address of a list."
		return view
	}
	var data []byte
	var err error
	switch {
	case strings.HasPrefix(source, "https://"):
		data, err = getMarket(ctx, source)
	case strings.Contains(source, "://"):
		err = errors.New("a market is read over https, or from a file on this machine")
	default:
		data, err = os.ReadFile(source)
		if err == nil && len(data) > marketMax {
			err = fmt.Errorf("the list is larger than %d bytes", marketMax)
		}
	}
	if err != nil {
		view.Error = "The market could not be read: " + err.Error()
		return view
	}
	var doc struct {
		Services []marketEntry `json:"services"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		view.Error = "The market is not a list that can be read: " + err.Error()
		return view
	}
	for _, e := range doc.Services {
		if checkEntry(&e) {
			view.Services = append(view.Services, e)
		} else {
			view.Skipped++
		}
	}
	slices.SortStableFunc(view.Services, func(a, b marketEntry) int { return strings.Compare(a.Name, b.Name) })
	return view
}

// getMarket reads the list at an https address, following no redirect away from https.
func getMarket(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if r.URL.Scheme != "https" || len(via) >= 5 {
			return errors.New("the list moved away from https")
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, marketMax+1))
	if err == nil && len(data) > marketMax {
		err = fmt.Errorf("the list is larger than %d bytes", marketMax)
	}
	return data, err
}

// checkEntry keeps an entry that says what it is in a way the page can show,
// and names a component in git with a tag, branch or commit.
func checkEntry(e *marketEntry) bool {
	e.Name, e.Description, e.Use = strings.TrimSpace(e.Name), strings.TrimSpace(e.Description), strings.TrimSpace(e.Use)
	if e.Name == "" || len(e.Name) > 60 || len(e.Description) > 300 || !workflow.IsRemote(e.Use) || len(e.Inputs) > 30 || len(e.Tags) > 10 {
		return false
	}
	if _, err := workflow.ParseRemote(e.Use); err != nil || !slices.Contains([]string{"ai", "decide", "http", "flow"}, e.Kind) {
		return false
	}
	for _, in := range e.Inputs {
		if !inputName.MatchString(in.Name) || !slices.Contains(workflow.ParamTypes, in.Type) {
			return false
		}
	}
	if e.Inputs == nil {
		e.Inputs = []inputView{}
	}
	return !slices.ContainsFunc(e.Tags, func(t string) bool { return !tagRE.MatchString(t) })
}

// lockFor is the lock file that holds for w: the one there is, or else the
// one flovr get would write when run in the folder the server was
// started with, unless the top of a git repository comes first.
func (s *Server) lockFor(w *workflow.Workflow) string {
	if path, found := workflow.FindLock(w.Path); found {
		return path
	}
	dir, err := filepath.Abs(filepath.Dir(w.Path))
	if err != nil {
		return filepath.Join(filepath.Dir(w.Path), workflow.LockFile)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return filepath.Join(dir, workflow.LockFile)
		}
		for _, root := range s.opts.Roots {
			if abs, err := filepath.Abs(root); err == nil && abs == dir {
				return filepath.Join(dir, workflow.LockFile)
			}
		}
		up := filepath.Dir(dir)
		if up == dir {
			return filepath.Join(filepath.Dir(w.Path), workflow.LockFile)
		}
		dir = up
	}
}

// takeFromGit makes sure the component use names is on this machine, before
// the server's lock is taken, since fetching goes to the network. It returns
// the commit to pin: the one the lock file pins, else the one the tag or
// branch is at now.
func (s *Server) takeFromGit(ctx context.Context, name, use string) (string, error) {
	i := slices.IndexFunc(s.live.Load().workflows, func(w *workflow.Workflow) bool { return w.Name == name })
	if i < 0 {
		return "", fmt.Errorf("no workflow is named %s", name)
	}
	r, err := workflow.ParseRemote(use)
	if err != nil {
		return "", err
	}
	commit := r.Ref
	if !workflow.IsCommit(commit) {
		lock, err := workflow.ReadLock(s.lockFor(s.live.Load().workflows[i]))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if commit = lock[r.Key()]; commit == "" {
			ctx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			if commit, err = fetch.Resolve(ctx, r); err != nil {
				return "", fmt.Errorf("%s: %v", use, err)
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if _, err := fetch.Get(ctx, r, commit); err != nil {
		return "", fmt.Errorf("%s: %v", use, err)
	}
	return commit, nil
}

// pinFromGit pins the component use names, fetched at commit, in the lock
// file that holds for w, and returns its input and how to put the lock file
// back as it was. The caller holds s.mu.
func (s *Server) pinFromGit(w *workflow.Workflow, use, commit string) ([]inputView, func(), error) {
	r, err := workflow.ParseRemote(use)
	if err != nil {
		return nil, nil, err
	}
	c, err := workflow.LoadComponent(r.File(commit))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, fmt.Errorf("%s: the repository has no component there", use)
	} else if err != nil {
		return nil, nil, fmt.Errorf("%s: %v", use, err)
	}
	inputs := []inputView{}
	for _, p := range c.Input {
		inputs = append(inputs, inputView{p.Name, p.Type})
	}
	undo := func() {}
	if workflow.IsCommit(r.Ref) {
		return inputs, undo, nil
	}
	path := s.lockFor(w)
	before, readErr := os.ReadFile(path)
	lock, err := workflow.ReadLock(path)
	if errors.Is(err, fs.ErrNotExist) {
		lock = workflow.Lock{}
	} else if err != nil {
		return nil, nil, err
	}
	if lock[r.Key()] == commit {
		return inputs, undo, nil
	}
	lock[r.Key()] = commit
	if err := lock.Write(path); err != nil {
		return nil, nil, err
	}
	undo = func() {
		if errors.Is(readErr, fs.ErrNotExist) {
			os.Remove(path)
		} else {
			os.WriteFile(path, before, 0o644)
		}
	}
	s.opts.Log("%s pinned at %.7s in %s", r.Key(), commit, path)
	return inputs, undo, nil
}

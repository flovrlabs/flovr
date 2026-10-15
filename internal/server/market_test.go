package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flovrlabs/flovr/internal/fetch"
	"github.com/flovrlabs/flovr/internal/workflow"
)

func TestMarketIsReadAndChecked(t *testing.T) {
	srv, _, _ := editFolder(t)
	list := filepath.Join(t.TempDir(), "index.json")
	os.WriteFile(list, []byte(`{"services": [
		{"name": "zeta", "use": "github.com/acme/services/zeta@v1", "kind": "http", "inputs": [{"name": "url", "type": "string"}], "tags": ["chat"]},
		{"name": "alpha", "description": "First.", "use": "github.com/acme/services/alpha@v2", "kind": "ai"},
		{"name": "local", "use": "../components/notify", "kind": "http"},
		{"name": "bad kind", "use": "github.com/acme/services/x@v1", "kind": "shell"},
		{"name": "bad input", "use": "github.com/acme/services/y@v1", "kind": "http", "inputs": [{"name": "1x", "type": "string"}]},
		{"name": "bad tag", "use": "github.com/acme/services/z@v1", "kind": "http", "tags": ["Not A Tag"]}
	]}`), 0o644)
	srv.opts.Market = list

	var view marketView
	if err := json.Unmarshal(get(srv, "/api/market").Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range view.Services {
		names = append(names, e.Name)
	}
	if got := strings.Join(names, " "); got != "alpha zeta" || view.Skipped != 4 || view.Error != "" {
		t.Errorf("market = %s, skipped %d, error %q", got, view.Skipped, view.Error)
	}
	if view.Services[0].Inputs == nil {
		t.Error("an entry with no input says nil, not none")
	}

	for source, want := range map[string]string{
		"":                                      "No market is set",
		"http://example.org/list.json":          "over https",
		filepath.Join(t.TempDir(), "none.json"): "could not be read",
	} {
		if got := readMarket(t.Context(), source); !strings.Contains(got.Error, want) {
			t.Errorf("%q: error %q, want %q", source, got.Error, want)
		}
	}
}

// marketRepo makes a repository holding a notify component tagged v1 and
// has the server fetch from it in place of github.com.
func marketRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	if err := os.CopyFS(filepath.Join(repo, "notify"), os.DirFS("../../examples/triage-ticket/components/notify")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"commit", "-q", "-m", "notify"}, {"tag", "v1"}} {
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.org", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	fetch.RepoURL = func(workflow.Remote) string { return repo }
	fetch.AllowFile = true
	t.Cleanup(func() { fetch.RepoURL = func(r workflow.Remote) string { return r.URL() }; fetch.AllowFile = false })
	t.Setenv("FLOVR_CACHE", t.TempDir())
	return repo
}

func TestStepFromTheMarketIsFetchedAndPinned(t *testing.T) {
	marketRepo(t)
	srv, root, _ := editFolder(t)
	lockPath := filepath.Join(root, workflow.LockFile)
	use := "github.com/acme/services/notify@v1"
	add := func(with string) *httptest.ResponseRecorder {
		rev := ""
		for _, w := range currentState(t, srv).Workflows {
			if w.Name == "moderate-comment" {
				rev = w.Rev
			}
		}
		body := `{"workflow": "moderate-comment", "rev": "` + rev + `", "add": {"id": "tell", "after": "check", "use": "` + use + `", "with": ` + with + `}}`
		return post(srv, "/api/steps", "", body)
	}

	// Refused for what the step gives it: the lock file is not left behind.
	if rec := add(`{"url": "https://example.org/hook"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Errorf("the lock file is left after a refusal: %v", err)
	}

	rec := add(`{"url": "https://example.org/hook", "summary": "${check.reason.value}", "category": "moderation"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	lock, err := workflow.ReadLock(lockPath)
	if err != nil || !workflow.IsCommit(lock["github.com/acme/services@v1"]) {
		t.Fatalf("lock = %v, %v", lock, err)
	}
	if text := readFile(t, filepath.Join(root, "moderate-comment", workflow.FileName)); !strings.Contains(text, "use: "+use) {
		t.Errorf("the workflow file does not use it:\n%s", text)
	}
	// It is shown as from git, and the workflow loads as the commands load it.
	if _, err := workflow.Load(filepath.Join(root, "moderate-comment")); err != nil {
		t.Errorf("the workflow does not load: %v", err)
	}
	shown := false
	for _, w := range currentState(t, srv).Workflows {
		shown = shown || (w.Name == use && w.Source == use)
	}
	if !shown {
		t.Error("the component is not shown as from git")
	}
}

// The market this repository keeps is one the page can read in full.
func TestOwnMarketIsValid(t *testing.T) {
	view := readMarket(t.Context(), "../../market/index.json")
	if view.Error != "" || view.Skipped != 0 || len(view.Services) == 0 {
		t.Errorf("market: %d services, %d skipped, error %q", len(view.Services), view.Skipped, view.Error)
	}
	for _, e := range view.Services {
		r, _ := workflow.ParseRemote(e.Use)
		if _, err := os.Stat(filepath.Join("../..", filepath.FromSlash(r.Dir), workflow.ComponentFile)); r.Repo == "flovr" && err != nil {
			t.Errorf("%s names a component this repository does not hold: %v", e.Use, err)
		}
	}
}

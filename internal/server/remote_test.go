package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/simonyosb/flowmaker/internal/workflow"
)

// servedFromGit serves a folder whose workflow takes notify from git, as
// fetched and pinned. It returns the server and the name the page knows
// notify by.
func servedFromGit(t *testing.T) (*Server, string) {
	t.Helper()
	commit := strings.Repeat("a", 40)
	t.Setenv("FLOWMAKER_CACHE", t.TempDir())
	repo := workflow.Remote{Host: "github.com", Owner: "acme", Repo: "services"}
	file := filepath.Join(repo.Checkout(commit), "notify", workflow.ComponentFile)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`name: notify
description: Tells a team.
input:
  url: string
http:
  method: POST
  url: ${input.url}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(t.TempDir(), "flows")
	if err := os.MkdirAll(filepath.Join(root, "hello"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.Mkdir(filepath.Join(root, ".git"), 0o755)
	if err := (workflow.Lock{"github.com/acme/services@v1": commit}).Write(filepath.Join(root, workflow.LockFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hello", workflow.FileName), []byte(`name: hello
trigger:
  webhook: /hello
steps:
  - id: tell
    use: github.com/acme/services/notify@v1
    with:
      url: https://example.org/hook
`), 0o644); err != nil {
		t.Fatal(err)
	}
	workflows, err := workflow.Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(workflows, Options{Model: fakeModel{"high"}, Edit: true, Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	return srv, "github.com/acme/services/notify@v1"
}

func TestComponentFromGitIsShownButNotChanged(t *testing.T) {
	srv, name := servedFromGit(t)
	var notify, hello *workflowView
	st := currentState(t, srv)
	for i := range st.Workflows {
		switch st.Workflows[i].Name {
		case name:
			notify = &st.Workflows[i]
		case "hello":
			hello = &st.Workflows[i]
		}
	}
	if notify == nil || hello == nil {
		t.Fatalf("workflows = %+v", st.Workflows)
	}
	if notify.Source != "github.com/acme/services/notify@v1" || len(notify.Steps[0].Edit) != 0 || notify.Cases != nil {
		t.Errorf("notify = %+v", notify)
	}
	// A workflow is offered it by the name it has in git.
	offered := false
	for _, c := range hello.Components {
		offered = offered || c.Use == "github.com/acme/services/notify@v1"
	}
	if !offered {
		t.Errorf("components offered = %+v", hello.Components)
	}

	body, _ := json.Marshal(map[string]any{"workflow": name, "rev": "x", "edits": []any{set("GET", "http", "method")}})
	rec := post(srv, "/api/edit", "", string(body))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "comes from git") {
		t.Errorf("an edit: status = %d, body %s", rec.Code, rec.Body)
	}
}

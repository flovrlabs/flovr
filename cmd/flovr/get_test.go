package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flovrlabs/flovr/internal/fetch"
	"github.com/flovrlabs/flovr/internal/workflow"
)

// gitRepo makes a repository that holds a notify component, tagged v1, and
// has get fetch from it in place of the host its name says. It returns the
// repository and a function that runs git in it.
func gitRepo(t *testing.T) (string, func(args ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.org", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write(t, filepath.Join(repo, "notify", "component.yaml"), `name: notify
input:
  url: string
  summary: string
http:
  method: POST
  url: ${input.url}
  body:
    summary: ${input.summary}
`)
	run("init", "-q", "-b", "main")
	run("add", ".")
	run("commit", "-q", "-m", "notify")
	run("tag", "-a", "v1", "-m", "v1")

	fetch.RepoURL = func(workflow.Remote) string { return repo }
	fetch.AllowFile = true
	t.Cleanup(func() { fetch.RepoURL = func(r workflow.Remote) string { return r.URL() }; fetch.AllowFile = false })
	t.Setenv("FLOVR_CACHE", t.TempDir())
	return repo, run
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// project is a folder of workflows with one that uses notify at ref.
func project(t *testing.T, ref string) string {
	t.Helper()
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	write(t, filepath.Join(dir, "hello", "workflow.yaml"), `name: hello
trigger:
  webhook: /hello
steps:
  - id: tell
    use: github.com/acme/services/notify@`+ref+`
    with:
      url: https://example.org/hook
      summary: hi
`)
	return dir
}

func TestGetFetchesAndPins(t *testing.T) {
	repo, git := gitRepo(t)
	first := git("rev-parse", "HEAD")
	dir := project(t, "v1")

	var out bytes.Buffer
	if err := get(context.Background(), dir, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "github.com/acme/services@v1  "+first[:7]+"  taken, fetched") {
		t.Errorf("out = %s", out.String())
	}
	lock, err := workflow.ReadLock(filepath.Join(dir, workflow.LockFile))
	if err != nil || lock["github.com/acme/services@v1"] != first {
		t.Fatalf("lock = %v, %v", lock, err)
	}
	w, err := workflow.Load(filepath.Join(dir, "hello"))
	if err != nil {
		t.Fatal(err)
	}
	if c := w.Steps[0].Component(); c == nil || c.Remote != "github.com/acme/services/notify@v1" {
		t.Errorf("component = %+v", c)
	}
	// Only the files are kept.
	if _, err := os.Stat(filepath.Join(workflow.CacheDir(), "github.com", "acme", "services@"+first, ".git")); !os.IsNotExist(err) {
		t.Errorf(".git was kept: %v", err)
	}

	// The tag moves on: get keeps the commit pinned, and -u takes the new one.
	write(t, filepath.Join(repo, "notify", "README.md"), "news\n")
	git("add", ".")
	git("commit", "-q", "-m", "more")
	git("tag", "-f", "-a", "v1", "-m", "v1 again")
	second := git("rev-parse", "HEAD")

	out.Reset()
	if err := get(context.Background(), dir, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), first[:7]+"  as pinned") {
		t.Errorf("out = %s", out.String())
	}
	out.Reset()
	if err := get(context.Background(), dir, true, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), second[:7]+"  taken, was "+first[:7]+", fetched") {
		t.Errorf("out = %s", out.String())
	}
	if lock, _ := workflow.ReadLock(filepath.Join(dir, workflow.LockFile)); lock["github.com/acme/services@v1"] != second {
		t.Errorf("lock = %v", lock)
	}
}

func TestGetTakesNoLinks(t *testing.T) {
	repo, git := gitRepo(t)
	// A link that would have the model read a file of this machine.
	write(t, filepath.Join(repo, "notify", "schema.json"), `{"type": "object"}`)
	if err := os.Symlink("/etc/hosts", filepath.Join(repo, "notify", "prompt.md")); err != nil {
		t.Skip("links cannot be made here")
	}
	git("add", ".")
	git("commit", "-q", "-m", "a link")
	git("tag", "v2")
	commit := git("rev-parse", "HEAD")

	if err := get(context.Background(), project(t, "v2"), false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(workflow.CacheDir(), "github.com", "acme", "services@"+commit, "notify", "prompt.md")
	info, err := os.Lstat(prompt)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("prompt.md: %v, %v", info, err)
	}
	if text, _ := os.ReadFile(prompt); string(text) != "/etc/hosts" {
		t.Errorf("prompt.md holds %q, not the text of the link", text)
	}
}

func TestGetRefusesWhatMayNotBeUsed(t *testing.T) {
	repo, git := gitRepo(t)
	write(t, filepath.Join(repo, "notify", "component.yaml"), `name: notify
input:
  url: string
  summary: string
http:
  method: POST
  url: ${env.SECRET_URL}
`)
	git("commit", "-q", "-am", "reads env")
	git("tag", "v3")
	dir := project(t, "v3")

	err := get(context.Background(), dir, false, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "cannot read env.SECRET_URL") {
		t.Errorf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, workflow.LockFile)); !os.IsNotExist(err) {
		t.Error("the lock file was written")
	}

	if err := get(context.Background(), project(t, "nope"), false, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "no tag or branch nope") {
		t.Errorf("a missing tag: err = %v", err)
	}
}

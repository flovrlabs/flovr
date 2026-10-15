package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRemote(t *testing.T) {
	r, err := ParseRemote("github.com/acme/services/team/notify@v1.2")
	if err != nil {
		t.Fatal(err)
	}
	if r.Host != "github.com" || r.Owner != "acme" || r.Repo != "services" || r.Dir != "team/notify" || r.Ref != "v1.2" {
		t.Errorf("parsed %+v", r)
	}
	if r.Key() != "github.com/acme/services@v1.2" || r.URL() != "https://github.com/acme/services.git" || r.String() != "github.com/acme/services/team/notify@v1.2" {
		t.Errorf("key %s, url %s, string %s", r.Key(), r.URL(), r.String())
	}
	for _, use := range []string{
		"github.com/acme@v1",               // no repository
		"github.com/acme/services",         // no ref
		"github.com/acme/services@",        // an empty ref
		"github.com/acme/../etc@v1",        // a way out
		"github.com/acme/services/.git@v1", // a hidden folder
		"github.com/acme/services@-u",      // a ref git would take for a flag
		"github.com/acme/services@a..b",    // a range, not a ref
		"Not_A_Host.com/acme/services@v1",  // not a host name
		"github.com/acme/serv ices@v1",     // a space
	} {
		if _, err := ParseRemote(use); err == nil {
			t.Errorf("%s: parsed", use)
		}
	}
	for use, remote := range map[string]bool{
		"github.com/acme/services@v1": true,
		"../components/notify":        false,
		"components/notify":           false,
		"./x.y/z@v1":                  false,
		"/abs/path@v1":                false,
	} {
		if IsRemote(use) != remote {
			t.Errorf("IsRemote(%s) = %v", use, !remote)
		}
	}
}

const commit = "0123456789abcdef0123456789abcdef01234567"

// fetched lays out a project that uses a component from git, the lock file
// that pins it and the cache holding the repository, with the component's
// file holding comp. It returns the project's workflow folder.
func fetched(t *testing.T, comp string, extra map[string]string) string {
	t.Helper()
	cache := t.TempDir()
	t.Setenv("FLOVR_CACHE", cache)
	repo := filepath.Join(cache, "github.com", "acme", "services@"+commit)
	files := map[string]string{"notify/component.yaml": comp}
	for k, v := range extra {
		files[k] = v
	}
	for rel, text := range files {
		path := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (Lock{"github.com/acme/services@v1": commit}).Write(filepath.Join(project, LockFile)); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(project, "hello")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	wf := `name: hello
trigger:
  webhook: /hello
steps:
  - id: tell
    use: github.com/acme/services/notify@v1
    with:
      url: https://example.org/hook
      summary: hi
`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(wf), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const notifyFromGit = `name: notify
input:
  url: string
  summary: string
http:
  method: POST
  url: ${input.url}
  body:
    summary: ${input.summary}
`

func TestComponentFromGitIsTakenAtThePinnedCommit(t *testing.T) {
	dir := fetched(t, notifyFromGit, nil)
	w, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := w.Steps[0].Component()
	if c == nil || c.Remote != "github.com/acme/services/notify@v1" || !strings.Contains(c.Path, "services@"+commit) {
		t.Fatalf("component = %+v", c)
	}
}

func TestComponentFromGitMustBeFetched(t *testing.T) {
	dir := fetched(t, notifyFromGit, nil)
	project := filepath.Dir(dir)

	// Not on this machine.
	t.Setenv("FLOVR_CACHE", t.TempDir())
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "not on this machine: run flovr get") {
		t.Errorf("err = %v", err)
	}
	// Not in the lock file.
	if err := (Lock{"github.com/acme/other@v1": commit}).Write(filepath.Join(project, LockFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "is not in") {
		t.Errorf("err = %v", err)
	}
	// No lock file, up to the top of the repository.
	os.Remove(filepath.Join(project, LockFile))
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "has not been fetched: run flovr get") {
		t.Errorf("err = %v", err)
	}
}

func TestComponentFromGitGetsOnlyItsInputAndItsOwnFiles(t *testing.T) {
	for name, tc := range map[string]struct {
		comp  string
		extra map[string]string
		want  string
	}{
		"env": {
			comp: strings.Replace(notifyFromGit, "url: ${input.url}", "url: ${env.SECRET_URL}", 1),
			want: "cannot read env.SECRET_URL",
		},
		"a prompt outside": {
			comp: `name: notify
input:
  url: string
  summary: string
ai:
  model: openrouter/acme/model
  prompt: ../../../../../../secret.md
  input: ${input.summary}
  output: schema.json
`,
			extra: map[string]string{"notify/schema.json": `{"type": "object"}`},
			want:  "secret.md is outside the repository it comes from",
		},
		"a component outside": {
			comp: `name: notify
input:
  url: string
  summary: string
steps:
  - id: inner
    use: ../../../../../elsewhere
output:
  done: true
`,
			want: "outside the repository this component comes from",
		},
		"another from git": {
			comp: `name: notify
input:
  url: string
  summary: string
steps:
  - id: inner
    use: github.com/someone/else/thing@v1
output:
  done: true
`,
			want: "cannot use another one from git",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := fetched(t, tc.comp, tc.extra)
			if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLockFileRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), LockFile)
	want := Lock{"github.com/b/x@v2": commit, "github.com/a/y@main": strings.Repeat("f", 40)}
	if err := want.Write(path); err != nil {
		t.Fatal(err)
	}
	text, _ := os.ReadFile(path)
	if !strings.Contains(string(text), "github.com/a/y@main "+strings.Repeat("f", 40)+"\ngithub.com/b/x@v2 ") {
		t.Errorf("not in order:\n%s", text)
	}
	got, err := ReadLock(path)
	if err != nil || len(got) != 2 || got["github.com/b/x@v2"] != commit {
		t.Errorf("read %v, %v", got, err)
	}
	os.WriteFile(path, []byte("github.com/a/y@main not-a-commit\n"), 0o644)
	if _, err := ReadLock(path); err == nil {
		t.Error("a bad line was read")
	}
}

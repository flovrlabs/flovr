// Package fetch takes repositories that components are kept in from git,
// into the cache where loading finds them.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/simonyosb/flowmaker/internal/workflow"
)

// For tests: where a repository is fetched from, and whether git may read a
// repository from a folder.
var (
	RepoURL   = func(r workflow.Remote) string { return r.URL() }
	AllowFile = false
)

// gitIn runs git in dir. A repository from elsewhere cannot make it run
// anything, write a link, or go anywhere but over https, and git never waits
// for a password to be typed.
func gitIn(ctx context.Context, dir string, args ...string) (string, error) {
	safe := []string{
		"-c", "core.fsmonitor=false", "-c", "core.hooksPath=" + os.DevNull, "-c", "core.symlinks=false",
		"-c", "protocol.allow=never", "-c", "protocol.https.allow=always",
	}
	if AllowFile {
		safe = append(safe, "-c", "protocol.file.allow=always")
	}
	cmd := exec.CommandContext(ctx, "git", append(safe, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return "", errors.New(strings.TrimSpace(string(exit.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Resolve returns the commit a tag or branch of the repository is at. A tag
// comes before a branch of the same name.
func Resolve(ctx context.Context, r workflow.Remote) (string, error) {
	// Every tag and branch is listed: asked for by name, git leaves out
	// the commit an annotated tag points at.
	out, err := gitIn(ctx, os.TempDir(), "ls-remote", "--tags", "--heads", "--", RepoURL(r))
	if err != nil {
		return "", err
	}
	found := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if commit, name, ok := strings.Cut(line, "\t"); ok {
			found[name] = commit
		}
	}
	for _, name := range []string{"refs/tags/" + r.Ref + "^{}", "refs/tags/" + r.Ref, "refs/heads/" + r.Ref} {
		if commit := found[name]; workflow.IsCommit(commit) {
			return commit, nil
		}
	}
	return "", fmt.Errorf("the repository has no tag or branch %s", r.Ref)
}

// Get puts the repository at commit in the cache, unless it is there, and
// reports whether it fetched it. Only the files are kept, never a link.
func Get(ctx context.Context, r workflow.Remote, commit string) (bool, error) {
	dest := r.Checkout(commit)
	if _, err := os.Stat(dest); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return false, err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dest), ".fetching-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmp)
	if _, err := gitIn(ctx, tmp, "init", "-q"); err != nil {
		return false, err
	}
	if _, err := gitIn(ctx, tmp, "fetch", "-q", "--depth", "1", "--no-tags", RepoURL(r), commit); err != nil {
		// Some hosts give out only what a tag or branch points at: the tag
		// or branch is taken, and must still be at the commit.
		if workflow.IsCommit(r.Ref) {
			return false, err
		}
		if _, err := gitIn(ctx, tmp, "fetch", "-q", "--depth", "1", "--no-tags", RepoURL(r), r.Ref); err != nil {
			return false, err
		}
	}
	got, err := gitIn(ctx, tmp, "rev-parse", "FETCH_HEAD^{commit}")
	if err != nil {
		return false, err
	}
	if got != commit {
		return false, fmt.Errorf("%s is at %.7s now, not at the pinned %.7s: run flowmaker get -u to take it again", r.Ref, got, commit)
	}
	if _, err := gitIn(ctx, tmp, "checkout", "-q", "--detach", "FETCH_HEAD"); err != nil {
		return false, err
	}
	if err := os.RemoveAll(filepath.Join(tmp, ".git")); err != nil {
		return false, err
	}
	err = filepath.WalkDir(tmp, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("the repository holds a link, %s, which is not taken", strings.TrimPrefix(path, tmp+string(filepath.Separator)))
		}
		return err
	})
	if err != nil {
		return false, err
	}
	if err := os.Rename(tmp, dest); err != nil {
		// Fetched at the same time by another get: what is there is the same.
		if _, statErr := os.Stat(dest); statErr == nil {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

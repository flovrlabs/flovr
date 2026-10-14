package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/simonyosb/flowmaker/internal/workflow"
)

func getCmd() *cobra.Command {
	var update bool
	cmd := &cobra.Command{
		Use:   "get [folder]",
		Short: "Fetch the components workflows take from git, and pin them in flowmaker.lock",
		Long: "Fetch the components that the workflows in a folder take from git, as in\n" +
			"use: github.com/acme/services/notify@v1.2, and write the commit each tag or\n" +
			"branch is at in flowmaker.lock in that folder. Keep flowmaker.lock in git with the\n" +
			"workflows: on every machine get then takes the same commits, until -u takes each\n" +
			"tag or branch again. Running and serving workflows never fetches anything.\n\n" +
			"A component from git reads only its input, never env, names only files in its own\n" +
			"repository and uses no other component from git.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			return get(cmd.Context(), dir, update, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVarP(&update, "update", "u", false, "take each tag or branch again instead of the commit it is pinned to")
	return cmd
}

// get fetches what the workflows and components in dir take from git, checks
// that each is a component that loads, and only then writes the lock file.
func get(ctx context.Context, dir string, update bool, out io.Writer) error {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("%s is not a folder", dir)
	}
	uses, err := usesFromGit(dir)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, workflow.LockFile)
	old, err := workflow.ReadLock(path)
	if errors.Is(err, fs.ErrNotExist) {
		old = workflow.Lock{}
	} else if err != nil {
		return err
	}
	if len(uses) == 0 {
		fmt.Fprintln(out, "No workflow here takes a component from git.")
		return nil
	}

	repos := map[string]workflow.Remote{}
	for _, r := range uses {
		repos[r.Key()] = r
	}
	lock, commits := workflow.Lock{}, map[string]string{}
	for _, key := range slices.Sorted(maps.Keys(repos)) {
		r := repos[key]
		commit, word := r.Ref, "pinned in its name"
		switch {
		case workflow.IsCommit(r.Ref):
		case !update && old[key] != "":
			commit, word = old[key], "as pinned"
		default:
			if commit, err = resolveRef(ctx, r); err != nil {
				return fmt.Errorf("%s: %v", key, err)
			}
			word = "taken"
			if was := old[key]; was != "" && was != commit {
				word = fmt.Sprintf("taken, was %.7s", was)
			}
		}
		if !workflow.IsCommit(r.Ref) {
			lock[key] = commit
		}
		commits[key] = commit
		fresh, err := fetch(ctx, r, commit)
		if err != nil {
			return fmt.Errorf("%s: %v", key, err)
		}
		if fresh {
			word += ", fetched"
		}
		fmt.Fprintf(out, "%s  %.7s  %s\n", key, commit, word)
	}

	// Each component named is there and is one that may be used.
	for _, use := range slices.Sorted(maps.Keys(uses)) {
		r := uses[use]
		file := r.File(commits[r.Key()])
		if _, err := os.Stat(file); err != nil {
			where := "its top folder"
			if r.Dir != "" {
				where = r.Dir
			}
			return fmt.Errorf("%s: the repository has no %s in %s", use, workflow.ComponentFile, where)
		}
		if _, err := workflow.LoadComponent(file); err != nil {
			return fmt.Errorf("%s: %v", use, err)
		}
	}
	if err := lock.Write(path); err != nil {
		return err
	}
	fmt.Fprintf(out, "Wrote %s.\n", path)
	return nil
}

// usesFromGit returns what the workflows and components in dir take from
// git, by how they name it. The files are read for that alone: one that
// does not load yet is reported by validate, not here.
func usesFromGit(dir string) (map[string]workflow.Remote, error) {
	uses := map[string]workflow.Remote{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != workflow.FileName && d.Name() != workflow.ComponentFile {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var doc struct {
			Steps []struct {
				Use string `yaml:"use"`
			} `yaml:"steps"`
		}
		if yaml.Unmarshal(data, &doc) != nil {
			return nil
		}
		for _, s := range doc.Steps {
			if !workflow.IsRemote(s.Use) {
				continue
			}
			r, err := workflow.ParseRemote(s.Use)
			if err != nil {
				return fmt.Errorf("%s: %v", path, err)
			}
			uses[s.Use] = r
		}
		return nil
	})
	return uses, err
}

// For tests: where a repository is fetched from, and whether git may read a
// repository from a folder.
var (
	repoURL   = func(r workflow.Remote) string { return r.URL() }
	allowFile = false
)

// gitIn runs git in dir. A repository from elsewhere cannot make it run
// anything, write a link, or go anywhere but over https, and git never waits
// for a password to be typed.
func gitIn(ctx context.Context, dir string, args ...string) (string, error) {
	safe := []string{
		"-c", "core.fsmonitor=false", "-c", "core.hooksPath=" + os.DevNull, "-c", "core.symlinks=false",
		"-c", "protocol.allow=never", "-c", "protocol.https.allow=always",
	}
	if allowFile {
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

// resolveRef returns the commit a tag or branch of the repository is at. A
// tag comes before a branch of the same name.
func resolveRef(ctx context.Context, r workflow.Remote) (string, error) {
	// Every tag and branch is listed: asked for by name, git leaves out
	// the commit an annotated tag points at.
	out, err := gitIn(ctx, os.TempDir(), "ls-remote", "--tags", "--heads", "--", repoURL(r))
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

// fetch puts the repository at commit in the cache, unless it is there, and
// reports whether it fetched it. Only the files are kept, never a link.
func fetch(ctx context.Context, r workflow.Remote, commit string) (bool, error) {
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
	if _, err := gitIn(ctx, tmp, "fetch", "-q", "--depth", "1", "--no-tags", repoURL(r), commit); err != nil {
		// Some hosts give out only what a tag or branch points at: the tag
		// or branch is taken, and must still be at the commit.
		if workflow.IsCommit(r.Ref) {
			return false, err
		}
		if _, err := gitIn(ctx, tmp, "fetch", "-q", "--depth", "1", "--no-tags", repoURL(r), r.Ref); err != nil {
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

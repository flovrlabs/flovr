package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/flovrlabs/flovr/internal/fetch"
	"github.com/flovrlabs/flovr/internal/workflow"
)

func getCmd() *cobra.Command {
	var update bool
	cmd := &cobra.Command{
		Use:   "get [folder]",
		Short: "Fetch the components workflows take from git, and pin them in flovr.lock",
		Long: "Fetch the components that the workflows in a folder take from git, as in\n" +
			"use: github.com/acme/services/notify@v1.2, and write the commit each tag or\n" +
			"branch is at in flovr.lock in that folder. Keep flovr.lock in git with the\n" +
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
			if commit, err = fetch.Resolve(ctx, r); err != nil {
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
		fresh, err := fetch.Get(ctx, r, commit)
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

package workflow

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Discover loads the workflow at each path. A path that is a directory is
// searched for every workflow file in it and below it, skipping hidden
// directories and testdata.
func Discover(paths ...string) ([]*Workflow, error) {
	var found []*Workflow
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			w, err := Load(path)
			if err != nil {
				return nil, err
			}
			found = append(found, w)
			continue
		}

		before := len(found)
		err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if p != path && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata") {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Name() != FileName {
				return nil
			}
			w, err := Load(p)
			if err != nil {
				return err
			}
			found = append(found, w)
			return nil
		})
		if err != nil {
			return nil, err
		}
		if len(found) == before {
			return nil, fmt.Errorf("no %s found in %s", FileName, path)
		}
	}
	return found, nil
}

package workflow

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// A step can use a component kept in a git repository, as
//
//	use: github.com/acme/flowmaker-services/notify@v1.2
//
// that is the host, the owner and the repository, then the component's
// folder in it unless it is at the top, and after @ the tag, branch or
// commit to take.
//
// Loading never goes to the network. flowmaker get fetches each repository
// at the commit its ref names into a cache on the machine, and writes that
// commit in a lock file, flowmaker.lock, which loading reads. The same files
// so give the same workflow on every machine, until the lock file changes.
//
// What comes from git is held to more than what is written by hand: it reads
// nothing but its input, never env, which holds the server's secrets; the
// files it names stay inside its own repository; and it uses no other
// component from git.

// LockFile is the file that pins each repository components are taken from
// to a commit. It holds for the workflows in its folder and the folders
// below.
const LockFile = "flowmaker.lock"

// Remote is a reference to a component in a git repository.
type Remote struct {
	Host, Owner, Repo string
	Dir               string // the component's folder in the repository, "" for the top
	Ref               string // a tag, a branch or a commit
}

var (
	hostRE   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
	partRE   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
	refRE    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`)
	commitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// IsRemote reports whether what a step uses names a component in git rather
// than a folder.
func IsRemote(use string) bool {
	host, _, _ := strings.Cut(use, "/")
	return strings.Contains(use, "@") && strings.Contains(host, ".") && !strings.HasPrefix(use, ".")
}

// ParseRemote reads a reference to a component in git.
func ParseRemote(use string) (Remote, error) {
	path, ref, _ := strings.Cut(use, "@")
	parts := strings.Split(path, "/")
	if len(parts) < 3 || ref == "" {
		return Remote{}, fmt.Errorf("%s: a component from git is named by host, owner and repository, then its folder if it has one, then @ and a tag, branch or commit, as in github.com/acme/services/notify@v1.2", use)
	}
	r := Remote{Host: parts[0], Owner: parts[1], Repo: parts[2], Dir: strings.Join(parts[3:], "/"), Ref: ref}
	switch {
	case !hostRE.MatchString(r.Host):
		return Remote{}, fmt.Errorf("%s: %q is not a host name", use, r.Host)
	case slices.ContainsFunc(parts[1:], func(p string) bool { return !partRE.MatchString(p) }):
		return Remote{}, fmt.Errorf("%s: an owner, repository or folder holds letters, digits, _, . and -, and starts with a letter, a digit or _", use)
	case len(ref) > 100 || !refRE.MatchString(ref) || strings.Contains(ref, "..") || strings.HasSuffix(ref, "/"):
		return Remote{}, fmt.Errorf("%s: %q is not a tag, branch or commit", use, ref)
	}
	return r, nil
}

// String is the reference as a step writes it.
func (r Remote) String() string {
	path := r.Host + "/" + r.Owner + "/" + r.Repo
	if r.Dir != "" {
		path += "/" + r.Dir
	}
	return path + "@" + r.Ref
}

// Key names the repository at the ref, which is what the lock file pins.
func (r Remote) Key() string { return r.Host + "/" + r.Owner + "/" + r.Repo + "@" + r.Ref }

// URL is where the repository is fetched from.
func (r Remote) URL() string { return "https://" + r.Host + "/" + r.Owner + "/" + r.Repo + ".git" }

// Checkout is the folder the repository is kept in, at commit.
func (r Remote) Checkout(commit string) string {
	return filepath.Join(CacheDir(), r.Host, r.Owner, r.Repo+"@"+commit)
}

// File is the component file of r, in the repository kept at commit.
func (r Remote) File(commit string) string {
	return filepath.Join(r.Checkout(commit), filepath.FromSlash(r.Dir), ComponentFile)
}

// CacheDir is the folder fetched repositories are kept in: $FLOWMAKER_CACHE,
// or flowmaker/services in the user's cache folder.
func CacheDir() string {
	if dir := os.Getenv("FLOWMAKER_CACHE"); dir != "" {
		return dir
	}
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "flowmaker", "services")
	}
	return filepath.Join(os.TempDir(), "flowmaker-services")
}

// IsCommit reports whether ref is a full commit hash, which needs no lock.
func IsCommit(ref string) bool { return commitRE.MatchString(ref) }

// Lock maps the Key of each repository to the commit it is pinned to.
type Lock map[string]string

// ReadLock reads a lock file.
func ReadLock(path string) (Lock, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lock := Lock{}
	scan := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; scan.Scan(); n++ {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, commit, ok := strings.Cut(line, " ")
		commit = strings.TrimSpace(commit)
		if !ok || !commitRE.MatchString(commit) {
			return nil, fmt.Errorf("%s:%d: a line is a repository with its ref, a space and a commit", path, n)
		}
		if _, err := ParseRemote(key); err != nil {
			return nil, fmt.Errorf("%s:%d: %v", path, n, err)
		}
		lock[key] = commit
	}
	return lock, nil
}

// Write writes the lock file, in the order of its keys, so that a change to
// it reads well in a diff.
func (l Lock) Write(path string) error {
	var b strings.Builder
	b.WriteString("# The commit each repository that components are taken from is pinned to.\n")
	b.WriteString("# Written by flowmaker get; keep it in git with the workflows.\n")
	for _, key := range slices.Sorted(maps.Keys(l)) {
		b.WriteString(key + " " + l[key] + "\n")
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// FindLock returns the lock file that holds for a file: the nearest in its
// folder or a folder above, no higher than the top of its git repository.
func FindLock(file string) (string, bool) {
	dir, err := filepath.Abs(filepath.Dir(file))
	if err != nil {
		return "", false
	}
	for {
		path := filepath.Join(dir, LockFile)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path, true
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return "", false
		}
		up := filepath.Dir(dir)
		if up == dir {
			return "", false
		}
		dir = up
	}
}

// remoteFile returns the component file that use names, for a step written in
// file: in the repository kept at the commit the lock file pins it to, or at
// the commit use names itself.
func remoteFile(file, use string) (string, error) {
	r, err := ParseRemote(use)
	if err != nil {
		return "", err
	}
	commit := r.Ref
	if !IsCommit(commit) {
		path, found := FindLock(file)
		if !found {
			return "", fmt.Errorf("%s has not been fetched: run flowmaker get, which fetches it and writes %s", use, LockFile)
		}
		lock, err := ReadLock(path)
		if err != nil {
			return "", err
		}
		if commit = lock[r.Key()]; commit == "" {
			return "", fmt.Errorf("%s is not in %s: run flowmaker get", use, path)
		}
	}
	if _, err := os.Stat(r.Checkout(commit)); errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%s is pinned to commit %.7s, which is not on this machine: run flowmaker get", use, commit)
	}
	return r.File(commit), nil
}

// checkoutOf returns the folder of the fetched repository that path is in,
// or "" if it is in none.
func checkoutOf(path string) string {
	cache, err := filepath.Abs(CacheDir())
	if err != nil {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(cache, abs)
	if err != nil || !filepath.IsLocal(rel) {
		return ""
	}
	// host/owner/repo@commit, then the files.
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) < 4 {
		return ""
	}
	return filepath.Join(cache, parts[0], parts[1], parts[2])
}

// FromGit reports whether the file at path is in a repository fetched from
// git, which is changed at its source and never here.
func FromGit(path string) bool { return checkoutOf(path) != "" }

// inside reports whether path is root or below it.
func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && filepath.IsLocal(rel)
}

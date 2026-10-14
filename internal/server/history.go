package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// historySize is how many runs the server keeps in memory to show.
const historySize = 500

var (
	// maxRecord is the longest a run's line in the file may be. A run that
	// would make a longer one is written without its request and outputs.
	maxRecord = 8 << 20
	// maxLine is the longest line read back; a longer one is passed over.
	maxLine = 64 << 20
)

// history holds the most recent runs, oldest first. Finished runs are also
// appended to a file, one JSON object per line, if it has one.
type history struct {
	mu   sync.Mutex
	runs []Run
	file *os.File
}

// openHistory returns a history that starts with the runs already in the file
// at path and appends to it. An empty path keeps runs in memory only.
func openHistory(path string) (*history, error) {
	h := &history{}
	if path == "" {
		return h, nil
	}
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		// The folder is usually inside a project, and what it holds is not
		// for git.
		os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*\n"), 0o600)
	}
	// Run records hold trigger payloads and step outputs, so only the owner
	// may read the file.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	reader := bufio.NewReader(f)
	for {
		data, err := reader.ReadBytes('\n')
		var r Run
		// A line that does not parse is a record cut short by a crash, and
		// one too long to read is passed over rather than keeping the
		// server from starting.
		if len(data) <= maxLine && json.Unmarshal(data, &r) == nil && r.ID != "" {
			h.runs = append(h.runs, r)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			f.Close()
			return nil, err
		}
	}
	h.trim()
	h.file = f
	return h, nil
}

// put records the current state of a run, replacing what it held for that id.
func (h *history) put(r Run) {
	r.Steps = slices.Clone(r.Steps)
	h.mu.Lock()
	defer h.mu.Unlock()

	if i := slices.IndexFunc(h.runs, func(x Run) bool { return x.ID == r.ID }); i >= 0 {
		h.runs[i] = r
	} else {
		h.runs = append(h.runs, r)
		h.trim()
	}
	if h.file != nil && r.Status != running {
		text := line(r)
		if len(text) > maxRecord {
			r.Trigger, r.Response, r.Gave = nil, nil, nil
			r.Steps = withoutOutputs(r.Steps)
			text = line(r)
		}
		h.file.Write(text)
	}
}

// line is a run as a line of the file.
func line(r Run) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// Escaping < > & would make a line up to six times longer for nothing.
	enc.SetEscapeHTML(false)
	enc.Encode(r)
	return buf.Bytes()
}

func (h *history) trim() {
	if extra := len(h.runs) - historySize; extra > 0 {
		h.runs = slices.Delete(h.runs, 0, extra)
	}
}

// recent returns the runs, newest first. With none it returns an empty
// list, which the page reads as a list.
func (h *history) recent() []Run {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := append([]Run{}, h.runs...)
	slices.Reverse(out)
	return out
}

func (h *history) get(id string) (Run, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	i := slices.IndexFunc(h.runs, func(x Run) bool { return x.ID == id })
	if i < 0 {
		return Run{}, false
	}
	return h.runs[i], true
}

func (h *history) close() {
	if h.file != nil {
		h.file.Close()
	}
}

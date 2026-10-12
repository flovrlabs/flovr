package server

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// historySize is how many runs the server keeps in memory to show.
const historySize = 500

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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// Run records hold trigger payloads and step outputs, so only the owner
	// may read the file.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(nil, 64<<20)
	for scanner.Scan() {
		var r Run
		// A line that does not parse is a record cut short by a crash.
		if json.Unmarshal(scanner.Bytes(), &r) == nil && r.ID != "" {
			h.runs = append(h.runs, r)
		}
	}
	if err := scanner.Err(); err != nil {
		f.Close()
		return nil, err
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
		if line, err := json.Marshal(r); err == nil {
			h.file.Write(append(line, '\n'))
		}
	}
}

func (h *history) trim() {
	if extra := len(h.runs) - historySize; extra > 0 {
		h.runs = slices.Delete(h.runs, 0, extra)
	}
}

// recent returns the runs, newest first.
func (h *history) recent() []Run {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := slices.Clone(h.runs)
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

package ai

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

// AnswersFile is the file, in the folder of a workflow or a component, that
// holds what the models answered in its tests, one answer to a line. flovr
// test --record writes it, and --replay answers from it without calling a
// model, so the tests and the page can be tried without a key.
const AnswersFile = "answers.jsonl"

// Answer is one answer of a model, kept under the request it answered.
type Answer struct {
	// Key is a digest of the whole request: the model, the prompt, the input
	// and the schema, or the questions. A request that differs in any of
	// them has another key, and gets no recorded answer.
	Key   string `json:"key"`
	Model string `json:"model"` // the model that answered
	// Input is the start of what the model was given, for people reading
	// the file. Replay does not look at it.
	Input  string          `json:"input"`
	Answer json.RawMessage `json:"answer"` // what the model wrote, or the answers it decided
}

type model interface {
	Generate(ctx context.Context, req Request) (Response, error)
	Decide(ctx context.Context, req DecideRequest) (DecideResponse, error)
}

func keyOf(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err) // requests are made of strings, maps and slices
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16])
}

func generateKey(req Request) string {
	return keyOf(struct {
		Kind, Model, System, Input string
		Schema                     map[string]any
	}{"write", req.Model, req.System, req.Input, req.Schema})
}

func decideKey(req DecideRequest) string {
	return keyOf(struct {
		Kind, Model string
		State       any
		Questions   map[string]Question
	}{"decide", req.Model, req.State, req.Questions})
}

// opening is the first line of input, cut short.
func opening(input any) string {
	text, ok := input.(string)
	if !ok {
		data, _ := json.Marshal(input)
		text = string(data)
	}
	// An input that is a JSON document is written on one line.
	var compact bytes.Buffer
	if json.Compact(&compact, []byte(text)) == nil {
		text = compact.String()
	}
	text, _, _ = strings.Cut(strings.TrimSpace(text), "\n")
	if utf8.RuneCountInString(text) > 80 {
		text = string([]rune(text)[:79]) + "…"
	}
	return text
}

// Recorder passes every request on to a model and keeps the answers.
type Recorder struct {
	next model
	mu   sync.Mutex
	kept map[string]Answer
}

func NewRecorder(next model) *Recorder {
	return &Recorder{next: next, kept: map[string]Answer{}}
}

func (r *Recorder) keep(a Answer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kept[a.Key] = a
}

func (r *Recorder) Generate(ctx context.Context, req Request) (Response, error) {
	resp, err := r.next.Generate(ctx, req)
	// An answer that is not JSON fails its step, so there is nothing to
	// replay.
	if err == nil && json.Valid([]byte(resp.Text)) {
		r.keep(Answer{Key: generateKey(req), Model: resp.Model, Input: opening(req.Input), Answer: json.RawMessage(resp.Text)})
	}
	return resp, err
}

func (r *Recorder) Decide(ctx context.Context, req DecideRequest) (DecideResponse, error) {
	resp, err := r.next.Decide(ctx, req)
	if err == nil {
		data, err := json.Marshal(resp.Answers)
		if err != nil {
			return DecideResponse{}, err
		}
		r.keep(Answer{Key: decideKey(req), Model: resp.Model, Input: opening(req.State), Answer: data})
	}
	return resp, err
}

// Write replaces the file at path with the answers kept, in the order of
// their keys, so that recording again changes only the lines of answers that
// changed. With no answers kept, it removes the file.
func (r *Recorder) Write(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.kept) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	var out strings.Builder
	for _, key := range slices.Sorted(maps.Keys(r.kept)) {
		line, err := json.Marshal(r.kept[key])
		if err != nil {
			return err
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(out.String()), 0o644)
}

// Len is how many answers have been kept.
func (r *Recorder) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.kept)
}

// Replay answers every request with an answer recorded before, and calls no
// model.
type Replay struct{ answers map[string]Answer }

// LoadAnswers reads every answers file in the folders at paths and in the
// folders below them. A path that is a file stands for its folder.
func LoadAnswers(paths ...string) (*Replay, error) {
	r := &Replay{answers: map[string]Answer{}}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			path = filepath.Dir(path)
		}
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
			if d.Name() == AnswersFile {
				return r.read(p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (r *Replay) read(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	lines := bufio.NewScanner(f)
	lines.Buffer(nil, 4<<20)
	for n := 1; lines.Scan(); n++ {
		if strings.TrimSpace(lines.Text()) == "" {
			continue
		}
		var a Answer
		if err := json.Unmarshal(lines.Bytes(), &a); err != nil || a.Key == "" || len(a.Answer) == 0 {
			return fmt.Errorf("%s:%d: not an answer as flovr test --record writes them", path, n)
		}
		r.answers[a.Key] = a
	}
	return lines.Err()
}

// Len is how many answers there are to replay.
func (r *Replay) Len() int { return len(r.answers) }

func notRecorded(model string) error {
	return &UnavailableError{fmt.Errorf("no answer of %s to this request was recorded: a prompt, an input or a schema may have changed since. "+
		"Record the answers again with flovr test --record, which calls the model", model)}
}

func (r *Replay) Generate(_ context.Context, req Request) (Response, error) {
	a, ok := r.answers[generateKey(req)]
	if !ok {
		return Response{}, notRecorded(req.Model)
	}
	return Response{Text: string(a.Answer), Model: "recorded from " + a.Model}, nil
}

func (r *Replay) Decide(_ context.Context, req DecideRequest) (DecideResponse, error) {
	a, ok := r.answers[decideKey(req)]
	if !ok {
		return DecideResponse{}, notRecorded(req.Model)
	}
	var answers map[string]any
	if err := json.Unmarshal(a.Answer, &answers); err != nil {
		return DecideResponse{}, fmt.Errorf("the recorded answer of %s is not a set of answers: %w", a.Model, err)
	}
	return DecideResponse{Answers: answers, Model: "recorded from " + a.Model}, nil
}

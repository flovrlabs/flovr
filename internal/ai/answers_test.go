package ai

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// answering answers every request the same way, and counts them.
type answering struct{ calls int }

func (a *answering) Generate(_ context.Context, req Request) (Response, error) {
	a.calls++
	text, _ := json.Marshal(map[string]string{"summary": req.Input})
	return Response{Text: string(text), Model: req.Model + "-2026", InputTokens: 10}, nil
}

func (a *answering) Decide(_ context.Context, req DecideRequest) (DecideResponse, error) {
	a.calls++
	return DecideResponse{Answers: map[string]any{"urgent": map[string]any{"value": true, "probability": 0.9}}, Model: req.Model}, nil
}

func TestRecordedAnswersAreReplayed(t *testing.T) {
	ctx := context.Background()
	write := Request{Model: "openrouter/m", System: "Summarize.", Input: "The site is down.\nSince noon.", Schema: map[string]any{"type": "object"}}
	decide := DecideRequest{Model: "openrouter/clef", State: map[string]any{"text": "help"},
		Questions: map[string]Question{"urgent": {Ask: "Is it urgent?", Kind: YesNo}}}

	model := &answering{}
	rec := NewRecorder(model)
	if _, err := rec.Generate(ctx, write); err != nil {
		t.Fatal(err)
	}
	if _, err := rec.Decide(ctx, decide); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, AnswersFile)
	if err := rec.Write(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if lines := strings.Split(strings.TrimSpace(string(data)), "\n"); len(lines) != 2 || !strings.Contains(string(data), `"input":"The site is down."`) {
		t.Errorf("answers file:\n%s", data)
	}

	replay, err := LoadAnswers(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := replay.Generate(ctx, write)
	if err != nil || got.Text != `{"summary":"The site is down.\nSince noon."}` || got.Model != "recorded from openrouter/m-2026" {
		t.Errorf("replayed %+v, %v", got, err)
	}
	decided, err := replay.Decide(ctx, decide)
	if err != nil || decided.Answers["urgent"].(map[string]any)["value"] != true {
		t.Errorf("replayed %+v, %v", decided, err)
	}
	if model.calls != 2 {
		t.Errorf("the model was called %d times; replay must not call it", model.calls)
	}

	// Any change to what is asked is a request that was not recorded.
	changed := []Request{write, write, write, write}
	changed[0].Model = "openrouter/other"
	changed[1].System = "Summarize briefly."
	changed[2].Input = "The site is up."
	changed[3].Schema = map[string]any{"type": "array"}
	for _, req := range changed {
		var unavailable *UnavailableError
		if _, err := replay.Generate(ctx, req); !errors.As(err, &unavailable) {
			t.Errorf("%+v: error = %v", req, err)
		}
	}
	decide.Questions = map[string]Question{"urgent": {Ask: "Is it very urgent?", Kind: YesNo}}
	if _, err := replay.Decide(ctx, decide); err == nil || !strings.Contains(err.Error(), "flowmaker test --record") {
		t.Errorf("error = %v", err)
	}
}

func TestAnswersAreFoundBelowAFolder(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	req := Request{Model: "openrouter/m", Input: "hi"}
	for _, dir := range []string{"one/components/reply", ".hidden"} {
		rec := NewRecorder(&answering{})
		rec.Generate(ctx, Request{Model: req.Model, Input: dir})
		os.MkdirAll(filepath.Join(root, dir), 0o755)
		if err := rec.Write(filepath.Join(root, dir, AnswersFile)); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(root, "one", "workflow.yaml"), nil, 0o644)
	replay, err := LoadAnswers(filepath.Join(root, "one", "workflow.yaml"), root)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Len() != 1 {
		t.Errorf("found %d answers, want the one outside hidden folders", replay.Len())
	}

	os.WriteFile(filepath.Join(root, "one", AnswersFile), []byte("{\"key\": \"k\"}\n"), 0o644)
	if _, err := LoadAnswers(root); err == nil || !strings.Contains(err.Error(), filepath.Join("one", AnswersFile)+":1:") {
		t.Errorf("error = %v", err)
	}
}

func TestNoAnswersLeavesNoFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), AnswersFile)
	os.WriteFile(path, []byte("old\n"), 0o644)
	if err := NewRecorder(&answering{}).Write(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the old file stays: %v", err)
	}
}

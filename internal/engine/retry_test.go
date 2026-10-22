package engine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flovrlabs/flovr/internal/ai"
	"github.com/flovrlabs/flovr/internal/workflow"
)

// flaky answers with the errors it is given, one per call, then with answer.
type flaky struct {
	errs   []error
	answer string
	calls  int
	block  bool // wait for the call to be cancelled instead of answering
}

func (m *flaky) Generate(ctx context.Context, req ai.Request) (ai.Response, error) {
	m.calls++
	if m.block {
		<-ctx.Done()
		return ai.Response{}, ctx.Err()
	}
	if m.calls <= len(m.errs) {
		if err := m.errs[m.calls-1]; err != nil {
			return ai.Response{}, err
		}
		return ai.Response{Text: "not json", Model: "m"}, nil
	}
	return ai.Response{Text: m.answer, Model: "m"}, nil
}

func (m *flaky) Decide(context.Context, ai.DecideRequest) (ai.DecideResponse, error) {
	return ai.DecideResponse{}, errors.New("not used")
}

const summarizeSchema = `{"type": "object", "properties": {"summary": {"type": "string"}}, "required": ["summary"], "additionalProperties": false}`

func summarizeFlow(t *testing.T, limits string) *workflow.Workflow {
	t.Helper()
	w, err := workflow.Load(writeProject(t, map[string]string{
		"workflow.yaml": "name: retrying\ntrigger:\n  webhook: /r\nsteps:\n  - id: summarize\n" + limits +
			"    ai: {model: anthropic/m, prompt: prompt.md, input: \"${trigger.body}\", output: schema.json}\n",
		"prompt.md":   "Summarize.",
		"schema.json": summarizeSchema,
	}))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func rateLimited() error {
	return &ai.UnavailableError{Err: &ai.StatusError{Status: http.StatusTooManyRequests}}
}

func TestAStepTriesAgainAfterTheFailuresItNames(t *testing.T) {
	ok := `{"summary": "down"}`
	tests := []struct {
		name   string
		limits string
		model  *flaky
		calls  int
		fails  string // "" when the step succeeds
	}{
		{"a rate limit, then an answer", "    retry: {times: 2, wait: 1ms}\n",
			&flaky{errs: []error{rateLimited()}, answer: ok}, 2, ""},
		{"an answer that does not fit, then one that does", "    retry: {times: 1, wait: 1ms, on: [invalid-answer]}\n",
			&flaky{errs: []error{nil}, answer: ok}, 2, ""},
		{"no retry, no second try", "",
			&flaky{errs: []error{rateLimited()}, answer: ok}, 1, "429"},
		{"a failure it does not name", "    retry: {times: 2, wait: 1ms, on: [unavailable]}\n",
			&flaky{errs: []error{rateLimited()}, answer: ok}, 1, "429"},
		{"a request that was refused is not tried again", "    retry: {times: 2, wait: 1ms}\n",
			&flaky{errs: []error{&ai.UnavailableError{Err: &ai.StatusError{Status: http.StatusUnauthorized}}}, answer: ok}, 1, "401"},
		{"it gives up after its tries", "    retry: {times: 2, wait: 1ms}\n",
			&flaky{errs: []error{rateLimited(), rateLimited(), rateLimited()}, answer: ok}, 3, "after 3 tries"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var res StepResult
			eng := &Engine{Model: tt.model, OnStep: func(r StepResult) { res = r }}
			_, err := eng.Run(context.Background(), summarizeFlow(t, tt.limits), map[string]any{"body": "The site is down."})
			if tt.model.calls != tt.calls {
				t.Errorf("the model was asked %d times, want %d", tt.model.calls, tt.calls)
			}
			if res.Tries != tt.calls {
				t.Errorf("tries = %d, want %d", res.Tries, tt.calls)
			}
			switch {
			case tt.fails == "" && err != nil:
				t.Errorf("run failed: %v", err)
			case tt.fails == "" && !strings.Contains(res.Detail, "on try 2"):
				t.Errorf("detail = %q", res.Detail)
			case tt.fails != "" && (err == nil || !strings.Contains(err.Error(), tt.fails)):
				t.Errorf("err = %v, want it to say %q", err, tt.fails)
			}
		})
	}
}

func TestATryStopsAtTheStepsTimeout(t *testing.T) {
	model := &flaky{block: true}
	var res StepResult
	eng := &Engine{Model: model, OnStep: func(r StepResult) { res = r }}
	start := time.Now()
	_, err := eng.Run(context.Background(), summarizeFlow(t, "    timeout: 50ms\n    retry: {times: 1, wait: 1ms, on: [timeout]}\n"), map[string]any{"body": "x"})
	if err == nil || !strings.Contains(err.Error(), "took longer than its timeout of 50ms") || !strings.Contains(err.Error(), "after 2 tries") {
		t.Fatalf("err = %v", err)
	}
	if model.calls != 2 || res.Tries != 2 {
		t.Errorf("calls = %d, tries = %d, want 2", model.calls, res.Tries)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("took %s", took)
	}
}

func TestAnHTTPStepTriesAgainAfterAnOutage(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"ok": true}`))
	}))
	t.Cleanup(srv.Close)
	w, err := workflow.Load(writeProject(t, map[string]string{
		"workflow.yaml": "name: retrying\ntrigger:\n  webhook: /r\nsteps:\n  - id: send\n    retry: {times: 1, wait: 1ms}\n" +
			"    http: {method: POST, url: \"${env.URL}\", body: {a: 1}}\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	eng := &Engine{HTTP: srv.Client(), Env: map[string]string{"URL": srv.URL}}
	if _, err := eng.Run(context.Background(), w, map[string]any{"body": map[string]any{}}); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("the server was called %d times, want 2", calls.Load())
	}
}

func TestTimeoutAndRetryAreChecked(t *testing.T) {
	tests := map[string]string{
		"    timeout: 0s\n":                           "timeout: must be more than 0",
		"    timeout: soon\n":                         "is not a length of time",
		"    retry: 0\n":                              "retry: times must be from 1 to 10",
		"    retry: {times: 2, on: [sometimes]}\n":    `"sometimes" is not one of`,
		"    retry: {times: 2, on: [timeout]}\n":      "needs the step to have a timeout",
		"    retry: {times: 2, wait: 10m}\n":          "wait must be at most",
		"    timeout: 30s\n    retry: 3\n":            "",
		"    timeout: 1m30s\n    retry: {times: 1}\n": "",
	}
	for limits, want := range tests {
		dir := writeProject(t, map[string]string{
			"workflow.yaml": "name: retrying\ntrigger:\n  webhook: /r\nsteps:\n  - id: summarize\n" + limits +
				"    ai: {model: anthropic/m, prompt: prompt.md, input: \"${trigger.body}\", output: schema.json}\n",
			"prompt.md":   "Summarize.",
			"schema.json": summarizeSchema,
		})
		_, err := workflow.Load(dir)
		switch {
		case want == "" && err != nil:
			t.Errorf("%q: %v", limits, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%q: err = %v, want %q", limits, err, want)
		}
	}
}

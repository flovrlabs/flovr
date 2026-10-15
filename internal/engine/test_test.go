package engine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/flovrlabs/flovr/internal/ai"
	"github.com/flovrlabs/flovr/internal/workflow"
)

const (
	refundLow  = `{"category": "refund", "priority": "low", "summary": "Duplicate charge."}`
	outageHigh = `{"category": "outage", "priority": "high", "summary": "Dashboard is down."}`
)

// testExample runs one test against the triage-ticket example with a model
// that gives answer. PAGER_URL points at a server that fails the Go test if
// anything reaches it.
func testExample(t *testing.T, test workflow.Test, answer string) (TestResult, error) {
	t.Helper()
	w, err := workflow.Load("testdata/triage-ticket")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("an http step was sent during a test")
	}))
	t.Cleanup(srv.Close)

	eng := &Engine{
		Model: &fakeModel{answer: answer},
		HTTP:  srv.Client(),
		Env:   map[string]string{"PAGER_URL": srv.URL},
	}
	return eng.Test(context.Background(), w, test)
}

func TestExampleTestsPass(t *testing.T) {
	w, err := workflow.Load("testdata/triage-ticket")
	if err != nil {
		t.Fatal(err)
	}
	for i, answer := range []string{refundLow, outageHigh} {
		res, err := testExample(t, w.Tests[i], answer)
		if err != nil {
			t.Fatal(err)
		}
		if res.Name != w.Tests[i].Name || len(res.Failures) != 0 || len(res.Steps) != 2 {
			t.Errorf("test %q: failures %q after %d steps", res.Name, res.Failures, len(res.Steps))
		}
	}
}

func TestExpectations(t *testing.T) {
	tests := []struct {
		name   string
		answer string
		expect map[string]any
		want   []string
	}{
		{
			name:   "wrong value and wrong step status",
			answer: outageHigh,
			expect: map[string]any{"classify.category": "refund", "escalate": "skipped"},
			want: []string{
				`classify.category: expected "refund", got "outage"`,
				`escalate: expected skipped, but it was called`,
			},
		},
		{
			name:   "step expected to be called was skipped",
			answer: refundLow,
			expect: map[string]any{"classify": "called", "escalate": "called"},
			want:   []string{`escalate: expected called, but it was skipped`},
		},
		{
			name:   "value of a skipped step",
			answer: refundLow,
			expect: map[string]any{"escalate.status": 200},
			want:   []string{`escalate.status: step "escalate" was skipped`},
		},
		{
			name:   "numbers, expressions and nested values compare by value",
			answer: outageHigh,
			expect: map[string]any{
				"escalate.status":                        200,
				"classify.summary.contains('down')":      true,
				"trigger.body.customer":                  "dana@example.com",
				"[classify.category, classify.priority]": []any{"outage", "high"},
			},
		},
		{
			name:   "key the output does not have",
			answer: outageHigh,
			expect: map[string]any{"classify.urgency": "high"},
			want:   []string{`classify.urgency: no such key: urgency`},
		},
		{
			name:   "run that fails",
			answer: `{"category": "spam", "priority": "high", "summary": "x"}`,
			expect: map[string]any{"classify.category": "spam"},
			want:   []string{`step "classify" failed: the model's answer does not match schemas/ticket.json`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := testExample(t, workflow.Test{Fixture: "fixtures/refund.json", Expect: tt.expect}, tt.answer)
			if err != nil {
				t.Fatal(err)
			}
			if res.Name != "fixtures/refund.json" {
				t.Errorf("name = %q, want the fixture path for an unnamed test", res.Name)
			}
			if len(res.Failures) != len(tt.want) {
				t.Fatalf("failures = %q, want %q", res.Failures, tt.want)
			}
			for i := range tt.want {
				if !strings.HasPrefix(res.Failures[i], tt.want[i]) {
					t.Errorf("failure %d = %q, want %q", i+1, res.Failures[i], tt.want[i])
				}
			}
		})
	}
}

func TestUnsetEnvVarsGetPlaceholders(t *testing.T) {
	w, err := workflow.Load("testdata/triage-ticket")
	if err != nil {
		t.Fatal(err)
	}
	eng := &Engine{Model: &fakeModel{answer: outageHigh}}
	res, err := eng.Test(context.Background(), w, w.Tests[1])
	if err != nil || len(res.Failures) != 0 {
		t.Errorf("failures = %q, error = %v; want a pass without PAGER_URL set", res.Failures, err)
	}
	if got, want := res.Steps[1].Detail, "POST, not sent"; got != want {
		t.Errorf("escalate detail = %q, want %q", got, want)
	}
	if eng.Env != nil {
		t.Errorf("Test changed the engine's Env to %v", eng.Env)
	}
}

func TestUnavailableModelIsAnErrorNotAFailure(t *testing.T) {
	w, err := workflow.Load("testdata/triage-ticket")
	if err != nil {
		t.Fatal(err)
	}
	eng := &Engine{Model: &fakeModel{err: &ai.UnavailableError{Err: errors.New("no credentials")}}}
	res, err := eng.Test(context.Background(), w, w.Tests[0])
	if err == nil || err.Error() != `step "classify" failed: no credentials` {
		t.Errorf("error = %v, want the model's error", err)
	}
	if len(res.Failures) != 0 {
		t.Errorf("failures = %q, want none", res.Failures)
	}

	// Anything else the model call returns is a failure of that test.
	eng = &Engine{Model: &fakeModel{err: errors.New("the model declined this request")}}
	res, err = eng.Test(context.Background(), w, w.Tests[0])
	want := []string{`step "classify" failed: the model declined this request`}
	if err != nil || !reflect.DeepEqual(res.Failures, want) {
		t.Errorf("failures = %q, error = %v; want %q", res.Failures, err, want)
	}
}

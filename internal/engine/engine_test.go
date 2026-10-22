package engine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/flovrlabs/flovr/internal/ai"
	"github.com/flovrlabs/flovr/internal/workflow"
)

type fakeModel struct {
	answer    string         // what Generate returns
	decision  map[string]any // what Decide returns
	err       error
	got       ai.Request
	gotDecide ai.DecideRequest
}

func (m *fakeModel) Decide(_ context.Context, req ai.DecideRequest) (ai.DecideResponse, error) {
	m.gotDecide = req
	return ai.DecideResponse{Answers: m.decision, Model: "fake-decider", InputTokens: 7}, m.err
}

func (m *fakeModel) Generate(_ context.Context, req ai.Request) (ai.Response, error) {
	m.got = req
	return ai.Response{Text: m.answer, Model: "fake-model", InputTokens: 10, OutputTokens: 5}, m.err
}

// pager stands in for the service the example's escalate step calls.
type pager struct {
	status int
	bodies []string
}

func (p *pager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	p.bodies = append(p.bodies, r.Method+" "+r.URL.Path+" "+r.Header.Get("Content-Type")+" "+string(body))
	w.WriteHeader(p.status)
	w.Write([]byte(`{"queued": true}`))
}

// runExample runs the triage-ticket example with a model that gives answer and
// a pager that responds with pagerStatus.
func runExample(t *testing.T, fixture, answer string, pagerStatus int) ([]StepResult, *fakeModel, *pager, error) {
	t.Helper()
	w, err := workflow.Load("testdata/triage-ticket")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(w.Resolve(fixture))
	if err != nil {
		t.Fatal(err)
	}
	var trigger any
	if err := json.Unmarshal(data, &trigger); err != nil {
		t.Fatal(err)
	}

	p := &pager{status: pagerStatus}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)

	model := &fakeModel{answer: answer}
	var results []StepResult
	eng := &Engine{
		Model:  model,
		HTTP:   srv.Client(),
		Env:    map[string]string{"PAGER_URL": srv.URL + "/hooks/secret-token"},
		OnStep: func(r StepResult) { results = append(results, r) },
	}
	_, err = eng.Run(context.Background(), w, trigger)
	return results, model, p, err
}

func statuses(results []StepResult) []string {
	var out []string
	for _, r := range results {
		out = append(out, r.ID+"="+string(r.Status))
	}
	return out
}

func TestHighPriorityTicketIsEscalated(t *testing.T) {
	answer := `{"category": "outage", "priority": "high", "summary": "Dashboard is down."}`
	results, model, p, err := runExample(t, "fixtures/outage.json", answer, http.StatusAccepted)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := statuses(results), []string{"classify=ok", "escalate=ok"}; !reflect.DeepEqual(got, want) {
		t.Errorf("statuses = %v, want %v", got, want)
	}

	w, err := workflow.Load("testdata/triage-ticket")
	if err != nil {
		t.Fatal(err)
	}
	if model.got.Model != w.Steps[0].AI.Model || !strings.Contains(model.got.System, "You triage incoming customer support tickets.") {
		t.Errorf("model got model %q and system prompt %q", model.got.Model, model.got.System)
	}
	if !strings.Contains(model.got.Input, `"subject": "Dashboard returns 502 for our whole team"`) {
		t.Errorf("model input does not carry the ticket: %s", model.got.Input)
	}
	if model.got.Schema["type"] != "object" {
		t.Errorf("model got schema %v", model.got.Schema)
	}

	wantCall := `POST /hooks/secret-token application/json {"category":"outage","summary":"Dashboard is down."}`
	if len(p.bodies) != 1 || p.bodies[0] != wantCall {
		t.Errorf("pager calls = %q, want one %q", p.bodies, wantCall)
	}
	wantOutput := map[string]any{"status": http.StatusAccepted, "body": map[string]any{"queued": true}}
	if !reflect.DeepEqual(results[1].Output, wantOutput) {
		t.Errorf("escalate output = %#v, want %#v", results[1].Output, wantOutput)
	}
	if strings.Contains(results[1].Detail, "secret-token") || !strings.HasSuffix(results[1].Detail, "-> 202") {
		t.Errorf("escalate detail = %q, want the host and status without the URL path", results[1].Detail)
	}
}

func TestLowPriorityTicketIsNotEscalated(t *testing.T) {
	answer := `{"category": "refund", "priority": "low", "summary": "Duplicate charge."}`
	results, _, p, err := runExample(t, "fixtures/refund.json", answer, http.StatusOK)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := statuses(results), []string{"classify=ok", "escalate=skipped"}; !reflect.DeepEqual(got, want) {
		t.Errorf("statuses = %v, want %v", got, want)
	}
	if len(p.bodies) != 0 {
		t.Errorf("pager was called: %q", p.bodies)
	}
}

func TestFailuresStopTheRun(t *testing.T) {
	high := `{"category": "outage", "priority": "high", "summary": "Down."}`
	tests := []struct {
		name        string
		answer      string
		pagerStatus int
		want        string
		wantSteps   []string
	}{
		{
			name:      "answer breaks the schema",
			answer:    `{"category": "spam", "priority": "high", "summary": "Down."}`,
			want:      `step "classify" failed: the model's answer does not match schemas/ticket.json`,
			wantSteps: []string{"classify=failed"},
		},
		{
			name:      "answer is not JSON",
			answer:    `Sure! Here is the classification.`,
			want:      `step "classify" failed: the model's answer is not JSON`,
			wantSteps: []string{"classify=failed"},
		},
		{
			name:        "http step gets an error status",
			answer:      high,
			pagerStatus: http.StatusInternalServerError,
			want:        `step "escalate" failed: the server answered 500 Internal Server Error`,
			wantSteps:   []string{"classify=ok", "escalate=failed"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, _, p, err := runExample(t, "fixtures/outage.json", tt.answer, tt.pagerStatus)
			if err == nil || !strings.HasPrefix(err.Error(), tt.want) {
				t.Errorf("error = %v, want one starting with %q", err, tt.want)
			}
			if got := statuses(results); !reflect.DeepEqual(got, tt.wantSteps) {
				t.Errorf("statuses = %v, want %v", got, tt.wantSteps)
			}
			if tt.pagerStatus == 0 && len(p.bodies) != 0 {
				t.Errorf("pager was called after classify failed: %q", p.bodies)
			}
		})
	}
}

func TestUnreachableServerDoesNotLeakTheURL(t *testing.T) {
	w, err := workflow.Load("testdata/triage-ticket")
	if err != nil {
		t.Fatal(err)
	}
	eng := &Engine{
		Model: &fakeModel{answer: `{"category": "outage", "priority": "high", "summary": "Down."}`},
		HTTP:  http.DefaultClient,
		Env:   map[string]string{"PAGER_URL": "http://127.0.0.1:1/hooks/secret-token"},
	}
	_, err = eng.Run(context.Background(), w, map[string]any{"body": "x"})
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Errorf("error = %v, want a connection error without the URL path", err)
	}
}

func TestMissingEnvVarFailsTheStep(t *testing.T) {
	w, err := workflow.Load("testdata/triage-ticket")
	if err != nil {
		t.Fatal(err)
	}
	eng := &Engine{
		Model: &fakeModel{answer: `{"category": "outage", "priority": "high", "summary": "Down."}`},
		HTTP:  http.DefaultClient,
		Env:   map[string]string{},
	}
	_, err = eng.Run(context.Background(), w, map[string]any{"body": "x"})
	want := `step "escalate" failed: url: ${env.PAGER_URL}: PAGER_URL is not set in the environment`
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %s", err, want)
	}
}

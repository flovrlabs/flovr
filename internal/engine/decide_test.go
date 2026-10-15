package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/flovrlabs/flovr/internal/ai"
	"github.com/flovrlabs/flovr/internal/workflow"
)

const example = "../../examples/triage-ticket"

// decision is what a decision model would answer for the example's classify
// step, in the form steps see.
func decision(category, priority string, high float64) map[string]any {
	rest := (1 - high) / 2
	return map[string]any{
		"category": map[string]any{"value": category, "confidence": 0.9},
		"priority": map[string]any{
			"value": priority, "score": 2 * high, "confidence": 0.7,
			"probabilities": map[string]any{"low": rest, "medium": rest, "high": high},
		},
	}
}

func names(options []ai.Option) []string {
	var out []string
	for _, o := range options {
		out = append(out, o.Name)
	}
	return out
}

// runDecideExample runs the example on its outage fixture with a model that
// decides as given and writes a fixed summary. The pager and the billing
// alert go to the same recording server, at /pager and /billing.
func runDecideExample(t *testing.T, decided map[string]any) ([]StepResult, *fakeModel, *pager, any) {
	t.Helper()
	w, err := workflow.Load(example)
	if err != nil {
		t.Fatal(err)
	}
	trigger, err := w.Fixture("fixtures/outage.json")
	if err != nil {
		t.Fatal(err)
	}
	p := &pager{status: http.StatusOK}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)

	model := &fakeModel{answer: `{"summary": "Something is wrong."}`, decision: decided}
	var results []StepResult
	eng := &Engine{
		Model:  model,
		HTTP:   srv.Client(),
		Env:    map[string]string{"PAGER_URL": srv.URL + "/pager", "BILLING_URL": srv.URL + "/billing"},
		OnStep: func(r StepResult) { results = append(results, r) },
	}
	if _, err := eng.Run(context.Background(), w, trigger); err != nil {
		t.Fatal(err)
	}
	return results, model, p, trigger
}

func TestDecisionIsBuiltFromTheWorkflow(t *testing.T) {
	results, model, _, trigger := runDecideExample(t, decision("outage", "high", 0.9))

	if results[0].Kind != "decide" || results[0].Detail != "fake-decider, 7 tokens in" {
		t.Errorf("classify result = %q, %q", results[0].Kind, results[0].Detail)
	}
	req := model.gotDecide
	if req.Model != "openrouter/cloudflare/clef" {
		t.Errorf("decision model = %q", req.Model)
	}
	if want := trigger.(map[string]any)["body"]; !reflect.DeepEqual(req.State, want) {
		t.Errorf("state = %#v, want the ticket %#v", req.State, want)
	}
	category, priority := req.Questions["category"], req.Questions["priority"]
	if category.Kind != ai.Choice || category.Ask != "What does the customer need?" {
		t.Errorf("category question = %+v", category)
	}
	if got, want := names(category.Options), []string{"refund", "billing", "outage", "bug", "account", "other"}; !reflect.DeepEqual(got, want) {
		t.Errorf("category options = %v, want them in the order written: %v", got, want)
	}
	if priority.Kind != ai.Score || !reflect.DeepEqual(names(priority.Options), []string{"low", "medium", "high"}) {
		t.Errorf("priority question = %+v", priority)
	}
	if got, want := priority.Options[0].Meaning, "Questions, feedback, and requests with no time pressure."; got != want {
		t.Errorf("meaning of low = %q, want %q", got, want)
	}
}

func TestDecisionRoutesUrgentTickets(t *testing.T) {
	const summary = `"summary":"Something is wrong."}`
	tests := []struct {
		name     string
		decided  map[string]any
		statuses []string
		calls    []string
		summary  bool
	}{
		{
			name:     "urgent outage pages on-call",
			decided:  decision("outage", "high", 0.9),
			statuses: []string{"classify=ok", "summarize=ok", "alert_billing=skipped", "page_oncall=ok"},
			calls:    []string{`POST /pager application/json {"category":"outage",` + summary},
			summary:  true,
		},
		{
			name:     "urgent refund goes to billing",
			decided:  decision("refund", "high", 0.9),
			statuses: []string{"classify=ok", "summarize=ok", "alert_billing=ok", "page_oncall=skipped"},
			calls:    []string{`POST /billing application/json {"category":"refund",` + summary},
			summary:  true,
		},
		{
			name:     "urgent billing question goes to billing",
			decided:  decision("billing", "high", 0.9),
			statuses: []string{"classify=ok", "summarize=ok", "alert_billing=ok", "page_oncall=skipped"},
			calls:    []string{`POST /billing application/json {"category":"billing",` + summary},
			summary:  true,
		},
		{
			name:     "any other urgent ticket pages on-call",
			decided:  decision("account", "high", 0.9),
			statuses: []string{"classify=ok", "summarize=ok", "alert_billing=skipped", "page_oncall=ok"},
			calls:    []string{`POST /pager application/json {"category":"account",` + summary},
			summary:  true,
		},
		{
			name:     "a ticket that is not urgent goes nowhere",
			decided:  decision("refund", "medium", 0.4),
			statuses: []string{"classify=ok", "summarize=skipped", "alert_billing=skipped", "page_oncall=skipped"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, model, p, _ := runDecideExample(t, tt.decided)
			if got := statuses(results); !reflect.DeepEqual(got, tt.statuses) {
				t.Errorf("statuses = %v, want %v", got, tt.statuses)
			}
			if !reflect.DeepEqual(p.bodies, tt.calls) {
				t.Errorf("calls = %q, want %q", p.bodies, tt.calls)
			}
			if got := model.got.Model != ""; got != tt.summary {
				t.Errorf("summary model called: %v, want %v", got, tt.summary)
			}
		})
	}
}

func TestExampleTestsWithDecisions(t *testing.T) {
	w, err := workflow.Load(example)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		test     workflow.Test
		decided  map[string]any
		failures []string
	}{
		{w.Tests[0], decision("refund", "low", 0.05), nil},
		{w.Tests[1], decision("outage", "high", 0.9), nil},
		{w.Tests[2], decision("refund", "high", 0.9), nil},
		{
			w.Tests[0], decision("billing", "medium", 0.4),
			[]string{
				`classify.category.value: expected "refund", got "billing"`,
				`classify.priority.probabilities.high < 0.3: expected true, got false`,
			},
		},
		{
			w.Tests[2], decision("outage", "high", 0.9),
			[]string{
				`alert_billing: expected called, but it was skipped`,
				`classify.category.value: expected "refund", got "outage"`,
				`page_oncall: expected skipped, but it was called`,
			},
		},
	}
	for _, tt := range tests {
		eng := &Engine{Model: &fakeModel{answer: `{"summary": "Something is wrong."}`, decision: tt.decided}}
		res, err := eng.Test(context.Background(), w, tt.test)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(res.Failures, tt.failures) {
			t.Errorf("test %q: failures = %q, want %q", res.Name, res.Failures, tt.failures)
		}
	}
}

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flovrlabs/flovr/internal/ai"
	"github.com/flovrlabs/flovr/internal/workflow"
)

// serveToTest serves the triage-ticket example for editing, with the given
// model and with its http steps pointed at a recording server.
func serveToTest(t *testing.T, model interface {
	Generate(context.Context, ai.Request) (ai.Response, error)
	Decide(context.Context, ai.DecideRequest) (ai.DecideResponse, error)
}) (*Server, *calls) {
	t.Helper()
	w, err := workflow.Load("../../examples/triage-ticket")
	if err != nil {
		t.Fatal(err)
	}
	c := &calls{}
	sink := httptest.NewServer(c)
	t.Cleanup(sink.Close)
	srv, err := New([]*workflow.Workflow{w}, Options{
		Model: model, HTTP: sink.Client(), Edit: true,
		Env: map[string]string{"PAGER_URL": sink.URL + "/pager", "BILLING_URL": sink.URL + "/billing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, c
}

// startTests asks the server to run tests of the triage-ticket example, as
// the page would, and waits for them. which is the "test" part of the
// request, or empty for all tests.
func startTests(t *testing.T, srv *Server, which string) (*httptest.ResponseRecorder, []started) {
	t.Helper()
	body := `{"workflow": "triage-ticket"` + which + `}`
	rec := post(srv, "/api/test", "", body)
	var answer struct{ Runs []started }
	json.Unmarshal(rec.Body.Bytes(), &answer)
	srv.Wait()
	return rec, answer.Runs
}

func TestPageIsToldTheTests(t *testing.T) {
	srv, _ := editExample(t)
	cases := currentState(t, srv).Workflows[0].Cases
	if len(cases) != 3 || cases[0].Name != "refund request is not escalated" || cases[0].Fixture != "fixtures/refund.json" {
		t.Fatalf("cases = %+v", cases)
	}
	var keys []string
	for _, e := range cases[0].Expect {
		keys = append(keys, e.Key)
	}
	want := "alert_billing | classify.category.value | classify.priority.probabilities.high < 0.3 | page_oncall | summarize"
	if got := strings.Join(keys, " | "); got != want || cases[0].Expect[1].Want != "refund" || cases[0].Expect[2].Want != true {
		t.Errorf("what the first test expects = %s", mustJSON(cases[0].Expect))
	}
	if plain := currentState(t, serveExample(t, "low", "").srv).Workflows[0]; plain.Cases != nil {
		t.Errorf("a read-only server lists the tests: %+v", plain.Cases)
	}
}

func TestTestsRunAsRunsThatSendNothing(t *testing.T) {
	// Every ticket is an urgent outage to this model, so the workflow would
	// page on-call, were its http steps sent.
	srv, sent := serveToTest(t, fakeModel{"high"})

	rec, runs := startTests(t, srv, "")
	if rec.Code != http.StatusAccepted || len(runs) != 3 || runs[1].Test != "outage report pages on-call" {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if len(sent.seen) != 0 {
		t.Errorf("tests sent requests: %q", sent.seen)
	}

	// The second test expects just what this model answers.
	var passed Run
	json.Unmarshal(get(srv, "/api/runs/"+runs[1].Run).Body.Bytes(), &passed)
	if passed.Status != "ok" || passed.Source != "test" || passed.Test != runs[1].Test || passed.Error != "" || passed.Trigger == nil {
		t.Errorf("the test that passes = %+v", passed)
	}
	if len(passed.Checks) != 5 || len(passed.Steps) != 4 || passed.Steps[3].Detail != "POST, not sent" {
		t.Errorf("checks %+v, steps %+v", passed.Checks, passed.Steps)
	}
	for _, c := range passed.Checks {
		if c.Failure != "" {
			t.Errorf("a check of the test that passes failed: %+v", c)
		}
	}

	// The first expects a refund that is not urgent.
	var failed Run
	json.Unmarshal(get(srv, "/api/runs/"+runs[0].Run).Body.Bytes(), &failed)
	var failures []string
	for _, c := range failed.Checks {
		if c.Failure != "" {
			failures = append(failures, c.Failure)
		}
	}
	want := []string{
		`classify.category.value: expected "refund", got "outage"`,
		`classify.priority.probabilities.high < 0.3: no such key: probabilities`,
		`page_oncall: expected skipped, but it was called`,
		`summarize: expected skipped, but it was called`,
	}
	if failed.Status != "failed" || failed.Error != "" || strings.Join(failures, "\n") != strings.Join(want, "\n") {
		t.Errorf("the test that fails: status %s, error %q, failures:\n%s", failed.Status, failed.Error, strings.Join(failures, "\n"))
	}

	// The list of runs shows them, newest first, with what they checked.
	listed := currentState(t, srv).Runs
	if len(listed) != 3 || listed[0].ID != runs[2].Run || listed[2].Test != runs[0].Test || len(listed[2].Checks) != 5 || listed[2].Trigger != nil {
		t.Errorf("listed runs = %+v", listed)
	}

	// One test is started by its position.
	rec, runs = startTests(t, srv, `, "test": 2`)
	if rec.Code != http.StatusAccepted || len(runs) != 1 || runs[0].Test != "urgent refund goes to billing, not on-call" {
		t.Errorf("one test: status = %d, body %s", rec.Code, rec.Body)
	}
}

func TestTestsAreNotStartedWhenTheyCannotBe(t *testing.T) {
	srv, _ := editExample(t)
	for body, want := range map[string]int{
		`{"workflow": "triage-ticket", "test": 3}`:  http.StatusUnprocessableEntity,
		`{"workflow": "triage-ticket", "test": -1}`: http.StatusUnprocessableEntity,
		`{"workflow": "other"}`:                     http.StatusNotFound,
		`not json`:                                  http.StatusBadRequest,
	} {
		if rec := post(srv, "/api/test", "", body); rec.Code != want {
			t.Errorf("%s: status = %d, body %s; want %d", body, rec.Code, rec.Body, want)
		}
	}
	if runs := currentState(t, srv).Runs; len(runs) != 0 {
		t.Errorf("%d runs were started", len(runs))
	}
	// A server that is not for editing does not start tests: they call
	// models, which whoever may see the page should not be able to cause.
	if rec := post(serveExample(t, "low", "").srv, "/api/test", "", `{"workflow": "triage-ticket"}`); rec.Code != http.StatusNotFound {
		t.Errorf("on a read-only server: status = %d", rec.Code)
	}

	// A workflow without tests says where they would be written.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "workflow.yaml"), []byte("name: bare\ntrigger: {webhook: /bare}\nsteps:\n  - id: ping\n    http: {method: GET, url: \"http://unused.invalid\"}\n"), 0o644)
	w, err := workflow.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	bare, err := New([]*workflow.Workflow{w}, Options{Edit: true})
	if err != nil {
		t.Fatal(err)
	}
	if rec := post(bare, "/api/test", "", `{"workflow": "bare"}`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "has no tests") {
		t.Errorf("no tests: status = %d, body %s", rec.Code, rec.Body)
	}
}

// offline is a model that cannot be reached.
type offline struct{}

func (offline) Generate(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{}, &ai.UnavailableError{Err: errors.New("no credentials")}
}

func (offline) Decide(context.Context, ai.DecideRequest) (ai.DecideResponse, error) {
	return ai.DecideResponse{}, &ai.UnavailableError{Err: errors.New("no credentials")}
}

func TestTestThatCouldNotRunSaysSo(t *testing.T) {
	srv, _ := serveToTest(t, offline{})
	_, runs := startTests(t, srv, `, "test": 0`)
	var run Run
	json.Unmarshal(get(srv, "/api/runs/"+runs[0].Run).Body.Bytes(), &run)
	if run.Status != "failed" || !strings.HasPrefix(run.Error, "The test could not run, which says nothing about the workflow:") || len(run.Checks) != 0 {
		t.Errorf("run = %+v", run)
	}
}

func TestTestOfAnEndpointSeesItsAnswer(t *testing.T) {
	srv, _ := editEndpoint(t)
	rec := post(srv, "/api/test", "", `{"workflow": "moderate-comment", "test": 0}`)
	var answer struct{ Runs []started }
	json.Unmarshal(rec.Body.Bytes(), &answer)
	srv.Wait()
	var run Run
	json.Unmarshal(get(srv, "/api/runs/"+answer.Runs[0].Run).Body.Bytes(), &run)
	if run.Status != "ok" || run.Response == nil || run.Response.Status != http.StatusOK || mustJSON(run.Checks) != `[{"key":"response.body.allowed","want":true},{"key":"response.status","want":200}]` {
		t.Errorf("run = %+v, checks %s", run, mustJSON(run.Checks))
	}
}

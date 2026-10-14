package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func tryStep(t *testing.T, srv *Server, body string) (int, stepTestView, string) {
	t.Helper()
	rec := post(srv, "/api/steps/test", "", body)
	var view stepTestView
	json.Unmarshal(rec.Body.Bytes(), &view)
	return rec.Code, view, rec.Body.String()
}

func TestStepIsTriedWithTheDataOfARun(t *testing.T) {
	srv, _ := editUrgent(t)
	var run Run
	json.Unmarshal(post(srv, "/tickets?wait=true", "", ticket).Body.Bytes(), &run)
	if run.Status != "ok" {
		t.Fatalf("run = %+v", run)
	}
	sent := len(srv.history.recent())

	// summarize reads classify, whose answer comes from the run: the model
	// that writes is called, the one that decides is not.
	code, view, body := tryStep(t, srv, `{"workflow": "triage-ticket", "step": "summarize", "run": "`+run.ID+`"}`)
	if code != http.StatusOK || view.Step.Status != "ok" || !view.WouldRun || view.From != run.ID || mustJSON(view.Step.Output) != `{"summary":"Something is wrong."}` {
		t.Errorf("status = %d, body %s", code, body)
	}
	// An http step is not sent, as in a test.
	code, view, body = tryStep(t, srv, `{"workflow": "triage-ticket", "step": "page_oncall", "run": "`+run.ID+`"}`)
	if code != http.StatusOK || view.Step.Status != "ok" || !strings.Contains(view.Step.Detail, "not sent") {
		t.Errorf("status = %d, body %s", code, body)
	}
	// Nothing is recorded as a run.
	if n := len(srv.history.recent()); n != sent {
		t.Errorf("%d runs recorded, want %d", n, sent)
	}
}

func TestStepIsTriedEvenWhenItsConditionIsFalse(t *testing.T) {
	srv, _ := editExample(t) // the model finds nothing urgent
	var run Run
	json.Unmarshal(post(srv, "/tickets?wait=true", "", ticket).Body.Bytes(), &run)
	code, view, body := tryStep(t, srv, `{"workflow": "triage-ticket", "step": "summarize", "run": "`+run.ID+`"}`)
	if code != http.StatusOK || view.WouldRun || view.Step.Status != "ok" {
		t.Errorf("status = %d, body %s", code, body)
	}
}

func TestStepIsTriedWithAGivenRequest(t *testing.T) {
	srv, _ := editUrgent(t)
	// classify reads only the request.
	code, view, body := tryStep(t, srv, `{"workflow": "triage-ticket", "step": "classify", "trigger": {"body": `+ticket+`}}`)
	if code != http.StatusOK || view.Step.Status != "ok" || view.From != "" {
		t.Errorf("status = %d, body %s", code, body)
	}
	// summarize needs classify, which this request has not been through.
	code, view, body = tryStep(t, srv, `{"workflow": "triage-ticket", "step": "summarize", "trigger": {"body": `+ticket+`}}`)
	if code != http.StatusOK || view.Step.Status != "failed" || view.Step.Error == "" {
		t.Errorf("status = %d, body %s", code, body)
	}
}

func TestStepTrialsAreRefused(t *testing.T) {
	srv, _ := editUrgent(t)
	for _, tc := range []struct{ body, want string }{
		{`{"workflow": "nope", "step": "classify", "run": "r-1"}`, "no workflow"},
		{`{"workflow": "triage-ticket", "step": "nope", "run": "r-1"}`, "no step nope"},
		{`{"workflow": "triage-ticket", "step": "classify", "run": "r-missing"}`, "no run r-missing"},
		{`{"workflow": "triage-ticket", "step": "classify"}`, "Give the step a run"},
	} {
		if code, _, body := tryStep(t, srv, tc.body); code == http.StatusOK || !strings.Contains(body, tc.want) {
			t.Errorf("%s: status = %d, body %s", tc.body, code, body)
		}
	}
	// Only a server for editing tries steps, since that calls models.
	f := serveExample(t, "low", "")
	if rec := post(f.srv, "/api/steps/test", "", `{"workflow": "triage-ticket", "step": "classify", "trigger": {}}`); rec.Code != http.StatusNotFound {
		t.Errorf("without --edit: status = %d", rec.Code)
	}
}

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// revOf is the rev the page has of the workflow named name.
func revOf(t *testing.T, srv *Server, name string) string {
	t.Helper()
	for _, w := range currentState(t, srv).Workflows {
		if w.Name == name {
			return w.Rev
		}
	}
	t.Fatalf("no workflow %s", name)
	return ""
}

func bodyEdit(t *testing.T, srv *Server, field, value string, remove bool) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"workflow": "moderate-comment", "rev": revOf(t, srv, "moderate-comment"),
		"edits": []any{map[string]any{"path": []any{"respond", "body", field}, "value": value, "remove": remove, "node": "response"}}})
	return post(srv, "/api/edit", "", string(body))
}

func TestResponseBodyFieldsAreAddedAndTakenOut(t *testing.T) {
	srv, root, _ := editFolder(t)
	file := filepath.Join(root, "moderate-comment", "workflow.yaml")

	if rec := bodyEdit(t, srv, "checked_by", "flovr", false); rec.Code != http.StatusOK {
		t.Fatalf("add: status = %d, body %s", rec.Code, rec.Body)
	}
	if text := readFile(t, file); !strings.Contains(text, "    reason: ${check.reason.value}\n    checked_by: flovr\n") {
		t.Errorf("the new field is not last in the body:\n%s", text)
	}
	if rec := bodyEdit(t, srv, "checked_by", "", true); rec.Code != http.StatusOK {
		t.Fatalf("remove: status = %d, body %s", rec.Code, rec.Body)
	}
	if text := readFile(t, file); strings.Contains(text, "checked_by") || !strings.Contains(text, "    allowed: ${check.allowed.value}\n    reason: ${check.reason.value}\n") {
		t.Errorf("after taking out checked_by:\n%s", text)
	}
	// A field a test expects stays.
	if rec := bodyEdit(t, srv, "reason", "", true); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "response.body has no reason") || !strings.Contains(rec.Body.String(), `"line":0`) {
		t.Errorf("taking out reason: status = %d, body %s", rec.Code, rec.Body)
	}

	before := readFile(t, file)
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"a bad name":               bodyEdit(t, srv, "1st", "x", false),
		"no value":                 bodyEdit(t, srv, "note", "", false),
		"no such field":            bodyEdit(t, srv, "nothing", "", true),
		"a field it does not read": bodyEdit(t, srv, "late", "${nowhere.value}", false),
	} {
		if rec.Code == http.StatusOK {
			t.Errorf("%s: status = %d", name, rec.Code)
		}
	}
	if readFile(t, file) != before {
		t.Error("a refused change was written")
	}
}

func TestWhatStartsAWorkflowIsSwitched(t *testing.T) {
	srv, root, _ := editFolder(t)
	file := filepath.Join(root, "triage-ticket", "workflow.yaml")
	switchTo := func(name string, to map[string]string) *httptest.ResponseRecorder {
		body := map[string]any{"workflow": name, "rev": revOf(t, srv, name)}
		for k, v := range to {
			body[k] = v
		}
		data, _ := json.Marshal(body)
		return post(srv, "/api/trigger", "", string(data))
	}

	if rec := switchTo("triage-ticket", map[string]string{"cron": "0 9 * * *"}); rec.Code != http.StatusOK {
		t.Fatalf("to a schedule: status = %d, body %s", rec.Code, rec.Body)
	}
	if text := readFile(t, file); !strings.Contains(text, "trigger:\n  cron: 0 9 * * *\n") || strings.Contains(text, "webhook:") {
		t.Errorf("after switching to a schedule:\n%s", text)
	}
	if routes := strings.Join(srv.Routes(), "\n"); !strings.Contains(routes, "cron 0 9 * * *  ->  triage-ticket") {
		t.Errorf("routes = %s", routes)
	}
	if rec := switchTo("triage-ticket", map[string]string{"webhook": "/tickets"}); rec.Code != http.StatusOK {
		t.Fatalf("back to a request: status = %d, body %s", rec.Code, rec.Body)
	}
	if text := readFile(t, file); !strings.Contains(text, "trigger:\n  webhook: /tickets\n") || strings.Contains(text, "cron:") {
		t.Errorf("after switching back:\n%s", text)
	}

	// A workflow that answers its caller cannot run on a schedule.
	other := filepath.Join(root, "moderate-comment", "workflow.yaml")
	before := readFile(t, other)
	if rec := switchTo("moderate-comment", map[string]string{"cron": "0 9 * * *"}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("an endpoint to a schedule: status = %d, body %s", rec.Code, rec.Body)
	}
	if rec := switchTo("moderate-comment", map[string]string{"cron": "0 9 * * *", "webhook": "/x"}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("both at once: status = %d", rec.Code)
	}
	if readFile(t, other) != before {
		t.Error("a refused switch was written")
	}
}

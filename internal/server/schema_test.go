package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func schemaOfSummarize(t *testing.T, srv *Server) schemaView {
	t.Helper()
	rec := get(srv, "/api/schema?workflow=triage-ticket&step=summarize")
	var view schemaView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	return view
}

func TestAnswerFieldsAreReadAndSaved(t *testing.T) {
	srv, dir := editExample(t)
	view := schemaOfSummarize(t, srv)
	if view.File != "schemas/summary.json" || mustJSON(view.Fields) != `[{"name":"summary","type":"text","required":true}]` {
		t.Fatalf("view = %+v", view)
	}

	body, _ := json.Marshal(map[string]any{"workflow": "triage-ticket", "step": "summarize", "rev": view.Rev, "fields": []map[string]any{
		{"name": "summary", "type": "text", "required": true, "description": "One sentence"},
		{"name": "urgency", "type": "one of", "options": []string{"now", "today", "later"}},
		{"name": "teams", "type": "list of text"},
	}})
	rec := post(srv, "/api/schema", "", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	want := `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "summary": {
      "type": "string",
      "description": "One sentence"
    },
    "urgency": {
      "type": "string",
      "enum": [
        "now",
        "today",
        "later"
      ]
    },
    "teams": {
      "type": "array",
      "items": {
        "type": "string"
      }
    }
  },
  "required": [
    "summary"
  ],
  "additionalProperties": false
}
`
	if got := readFile(t, filepath.Join(dir, "schemas", "summary.json")); got != want {
		t.Errorf("file =\n%s", got)
	}
	// The workflow is served with the new fields: later steps may read them.
	for _, w := range currentState(t, srv).Workflows {
		if w.Name == "triage-ticket" && !strings.Contains(mustJSON(w.Steps[1].Outputs), "urgency") {
			t.Errorf("outputs of summarize = %v", w.Steps[1].Outputs)
		}
	}
	if again := schemaOfSummarize(t, srv); len(again.Fields) != 3 || again.Fields[1].Type != "one of" || len(again.Fields[1].Options) != 3 {
		t.Errorf("read back = %+v", again.Fields)
	}
}

func TestAnswerFieldsThatWouldBreakAStepAreRefused(t *testing.T) {
	srv, dir := editExample(t)
	before := readFile(t, filepath.Join(dir, "schemas", "summary.json"))
	view := schemaOfSummarize(t, srv)
	for _, tc := range []struct{ fields, want string }{
		// alert_billing and page_oncall read summarize.summary.
		{`[{"name": "gist", "type": "text", "required": true}]`, "summary"},
		{`[{"name": "1st", "type": "text"}]`, "cannot be a field's name"},
		{`[{"name": "a", "type": "text"}, {"name": "a", "type": "number"}]`, "two fields named a"},
		{`[{"name": "a", "type": "one of", "options": ["x"]}]`, "at least two options"},
		{`[{"name": "a", "type": "date"}]`, "unknown type"},
		{`[]`, "at least one field"},
	} {
		rec := post(srv, "/api/schema", "", `{"workflow": "triage-ticket", "step": "summarize", "rev": "`+view.Rev+`", "fields": `+tc.fields+`}`)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: status = %d, body %s", tc.fields, rec.Code, rec.Body)
		}
	}
	if got := readFile(t, filepath.Join(dir, "schemas", "summary.json")); got != before {
		t.Errorf("the file changed:\n%s", got)
	}
	// A stale page is refused too.
	if rec := post(srv, "/api/schema", "", `{"workflow": "triage-ticket", "step": "summarize", "rev": "old", "fields": [{"name": "summary", "type": "text"}]}`); rec.Code != http.StatusConflict {
		t.Errorf("stale: status = %d", rec.Code)
	}
}

func TestSchemaBeyondFieldsIsEditedAsText(t *testing.T) {
	srv, dir := editExample(t)
	view := schemaOfSummarize(t, srv)
	text := `{"type": "object", "properties": {"summary": {"type": "string", "minLength": 10}}, "required": ["summary"]}`
	body, _ := json.Marshal(map[string]any{"workflow": "triage-ticket", "step": "summarize", "rev": view.Rev, "text": text})
	rec := post(srv, "/api/schema", "", string(body))
	var saved schemaView
	json.Unmarshal(rec.Body.Bytes(), &saved)
	if rec.Code != http.StatusOK || saved.Fields != nil || readFile(t, filepath.Join(dir, "schemas", "summary.json")) != text+"\n" {
		t.Errorf("status = %d, body %s", rec.Code, rec.Body)
	}
	bad, _ := json.Marshal(map[string]any{"workflow": "triage-ticket", "step": "summarize", "rev": saved.Rev, "text": "{not json"})
	if rec := post(srv, "/api/schema", "", string(bad)); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("not JSON: status = %d", rec.Code)
	}
}

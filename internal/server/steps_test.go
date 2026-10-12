package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// steps posts an addition or removal of a step for the example as the page
// would. what is the "add" or "remove" part of the request.
func steps(t *testing.T, srv *Server, what string) *httptest.ResponseRecorder {
	t.Helper()
	rev := currentState(t, srv).Workflows[0].Rev
	return post(srv, "/api/steps", "", `{"workflow": "triage-ticket", "rev": "`+rev+`", `+what+`}`)
}

func TestPageIsToldWhichComponentsAStepCanBeAddedFrom(t *testing.T) {
	srv, _ := editExample(t)
	got := mustJSON(currentState(t, srv).Workflows[0].Components)
	want := `[{"name":"notify","description":"Tells a team about an urgent ticket by sending it a summary.","use":"components/notify","kind":"http",` +
		`"inputs":[{"name":"url","type":"string"},{"name":"summary","type":"string"},{"name":"category","type":"string"}]},` +
		`{"name":"reply","description":"Drafts a reply to the customer and sends it through a mail service.","use":"components/reply","kind":"flow",` +
		`"inputs":[{"name":"ticket","type":"object"},{"name":"url","type":"string"}]}]`
	if got != want {
		t.Errorf("components = %s\nwant %s", got, want)
	}
	// A server that does not allow editing does not list them.
	if plain := currentState(t, serveExample(t, "low", "").srv).Workflows[0]; plain.Components != nil {
		t.Errorf("a read-only server lists components: %+v", plain.Components)
	}
	// A workflow with no components in its folder has none listed.
	endpoint, _ := editEndpoint(t)
	if got := currentState(t, endpoint).Workflows[0].Components; len(got) != 0 {
		t.Errorf("components of a workflow without any = %+v", got)
	}
}

func TestStepIsAddedFromAComponent(t *testing.T) {
	srv, dir := editExample(t)
	file := filepath.Join(dir, "workflow.yaml")
	before := readFile(t, file)

	// Last, with a condition.
	rec := steps(t, srv, `"add": {"id": "tell_support", "use": "components/notify", "when": "classify.priority.value == \"medium\"",
		"with": {"url": " ${env.PAGER_URL} ", "summary": "Medium: ${trigger.body.subject}", "category": "${classify.category.value}"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	added := `
  - id: tell_support
    when: classify.priority.value == "medium"
    use: components/notify
    with:
      url: ${env.PAGER_URL}
      summary: 'Medium: ${trigger.body.subject}'
      category: ${classify.category.value}
`
	marker := "      category: ${classify.category.value}\n\n# In tests"
	want := strings.Replace(before, marker, "      category: ${classify.category.value}\n"+added+"\n# In tests", 1)
	if got := readFile(t, file); got != want {
		t.Fatalf("file after adding the step:\n%s\nwant:\n%s", got, want)
	}
	// The server serves it at once, and the page gets it described.
	var answer struct{ Workflow workflowView }
	json.Unmarshal(rec.Body.Bytes(), &answer)
	if n := len(answer.Workflow.Steps); n != 5 || answer.Workflow.Steps[4].ID != "tell_support" || answer.Workflow.Steps[4].Component != "notify" {
		t.Errorf("the answer describes %d steps, the last %+v", n, answer.Workflow.Steps[n-1])
	}
	if state := currentState(t, srv); len(state.Workflows[0].Steps) != 5 {
		t.Errorf("the server serves %d steps", len(state.Workflows[0].Steps))
	}

	// First, and behind a given step.
	if rec := steps(t, srv, `"add": {"id": "early", "after": "trigger", "use": "components/notify",
		"with": {"url": "${env.PAGER_URL}", "summary": "New ticket", "category": "new"}}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if rec := steps(t, srv, `"add": {"id": "after_summary", "after": "summarize", "use": "components/notify",
		"with": {"url": "${env.PAGER_URL}", "summary": "${summarize.summary}", "category": "x"}}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var ids []string
	for _, step := range currentState(t, srv).Workflows[0].Steps {
		ids = append(ids, step.ID)
	}
	if got, want := strings.Join(ids, " "), "early classify summarize after_summary alert_billing page_oncall tell_support"; got != want {
		t.Errorf("steps are now %s, want %s", got, want)
	}

	// Taking the three away again leaves the file as it was.
	for _, id := range []string{"early", "tell_support", "after_summary"} {
		if rec := steps(t, srv, `"remove": "`+id+`"`); rec.Code != http.StatusOK {
			t.Fatalf("removing %s: status = %d, body %s", id, rec.Code, rec.Body)
		}
	}
	if got := readFile(t, file); got != before {
		t.Errorf("file after removing them again:\n%s", got)
	}
}

func TestStepChangesAreRefusedWithoutTouchingTheFile(t *testing.T) {
	srv, dir := editExample(t)
	file := filepath.Join(dir, "workflow.yaml")
	before := readFile(t, file)
	with := `"with": {"url": "${env.PAGER_URL}", "summary": "s", "category": "c"}`

	for _, tt := range []struct {
		name   string
		what   string
		status int
		want   string
	}{
		{"a component the workflow does not have", `"add": {"id": "x", "use": "../somewhere/else", ` + with + `}`,
			http.StatusUnprocessableEntity, "is not one of this workflow's components"},
		{"an id that is taken", `"add": {"id": "classify", "use": "components/notify", ` + with + `}`,
			http.StatusUnprocessableEntity, "this id is already used by an earlier step"},
		{"an id that is not one", `"add": {"id": "tell support", "use": "components/notify", ` + with + `}`,
			http.StatusUnprocessableEntity, "id may contain only letters, digits and underscores"},
		{"no id", `"add": {"id": " ", "use": "components/notify", ` + with + `}`,
			http.StatusUnprocessableEntity, "id is required"},
		{"input the component needs is not given", `"add": {"id": "x", "use": "components/notify", "with": {"url": "${env.PAGER_URL}"}}`,
			http.StatusUnprocessableEntity, "with.summary is missing"},
		{"a condition that reads a later step", `"add": {"id": "x", "after": "trigger", "use": "components/notify", "when": "classify.priority.value == \"high\"", ` + with + `}`,
			http.StatusUnprocessableEntity, "undeclared reference to 'classify'"},
		{"an environment variable that is not set", `"add": {"id": "x", "use": "components/notify", "with": {"url": "${env.NOWHERE}", "summary": "s", "category": "c"}}`,
			http.StatusUnprocessableEntity, "NOWHERE"},
		{"a place behind a step that does not exist", `"add": {"id": "x", "after": "nothing", "use": "components/notify", ` + with + `}`,
			http.StatusUnprocessableEntity, "there is no step nothing"},
		{"removing a step that others read", `"remove": "classify"`,
			http.StatusUnprocessableEntity, "undeclared reference to 'classify'"},
		{"removing a step that a test expects", `"remove": "page_oncall"`,
			http.StatusUnprocessableEntity, "expect.page_oncall"},
		{"removing a step that does not exist", `"remove": "nothing"`,
			http.StatusUnprocessableEntity, "there is no step nothing to remove"},
		{"neither adding nor removing", `"x": 1`, http.StatusBadRequest, "adds a step or removes one"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := steps(t, srv, tt.what)
			if rec.Code != tt.status || !strings.Contains(rec.Body.String(), tt.want) {
				t.Errorf("status = %d, body %s; want %d and %q", rec.Code, rec.Body, tt.status, tt.want)
			}
			if readFile(t, file) != before {
				t.Error("the file was changed")
			}
		})
	}

	// A change for a file the page has not seen is refused, as for values.
	stale := `{"workflow": "triage-ticket", "rev": "0000000000000000", "remove": "page_oncall"}`
	if rec := post(srv, "/api/steps", "", stale); rec.Code != http.StatusConflict {
		t.Errorf("a stale change: status = %d", rec.Code)
	}
	// A server not started for editing has no such endpoint.
	if rec := post(serveExample(t, "low", "").srv, "/api/steps", "", stale); rec.Code != http.StatusNotFound {
		t.Errorf("on a read-only server: status = %d", rec.Code)
	}
}

func TestValuesAreWrittenAsTheirInputsTakeThem(t *testing.T) {
	for _, tt := range []struct {
		text, kind string
		want       any
	}{
		{"3", "number", 3},
		{"2.5", "number", 2.5},
		{"${a.count}", "number", "${a.count}"},
		{"true", "boolean", true},
		{"false", "boolean", false},
		{"1", "boolean", "1"},
		{"${a.urgent}", "boolean", "${a.urgent}"},
		{"3", "string", "3"},
		{"true", "any", "true"},
		{"${trigger.body}", "object", "${trigger.body}"},
	} {
		if got := written(tt.text, tt.kind); got != tt.want {
			t.Errorf("written(%q, %s) = %#v, want %#v", tt.text, tt.kind, got, tt.want)
		}
	}
}

// The page finds a step's values by its position, which another step holds
// once steps were added or removed. Told which step it means, the server
// refuses a change that would land in another.
func TestChangeMeantForAnotherStepIsRefused(t *testing.T) {
	srv, dir := editExample(t)
	file := filepath.Join(dir, "workflow.yaml")
	before := readFile(t, file)
	send := func(node string, path ...any) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"workflow": "triage-ticket", "rev": currentState(t, srv).Workflows[0].Rev,
			"edits": []any{map[string]any{"path": path, "value": "/elsewhere", "node": node}}})
		return post(srv, "/api/edit", "", string(body))
	}
	for _, tt := range []struct {
		node string
		path []any
	}{
		{"summarize", []any{"steps", 2, "with", "url"}},
		{"alert_billing", []any{"trigger", "webhook"}},
		{"response", []any{"trigger", "webhook"}},
	} {
		if rec := send(tt.node, tt.path...); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "steps of the file changed") {
			t.Errorf("%s at %v: status = %d, body %s", tt.node, tt.path, rec.Code, rec.Body)
		}
	}
	if readFile(t, file) != before {
		t.Error("the file was changed")
	}
	if rec := send("trigger", "trigger", "webhook"); rec.Code != http.StatusOK {
		t.Errorf("the right owner: status = %d, body %s", rec.Code, rec.Body)
	}
}

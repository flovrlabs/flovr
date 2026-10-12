package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aia/internal/workflow"
)

// editUrgent serves a copy of the triage-ticket example for editing, with a
// model to which every ticket is an urgent outage, and returns the copy's
// folder.
func editUrgent(t *testing.T) (*Server, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "triage-ticket")
	if err := os.CopyFS(dir, os.DirFS("../../examples/triage-ticket")); err != nil {
		t.Fatal(err)
	}
	w, err := workflow.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	sink := httptest.NewServer(&calls{})
	t.Cleanup(sink.Close)
	srv, err := New([]*workflow.Workflow{w}, Options{
		Model: fakeModel{"high"}, HTTP: sink.Client(), Edit: true,
		Env: map[string]string{"PAGER_URL": sink.URL + "/pager", "BILLING_URL": sink.URL + "/billing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, dir
}

// cases posts a request about the example's tests, as the page would. what is
// the "draft", "add" or "remove" part.
func casesOf(t *testing.T, srv *Server, name, what string) *httptest.ResponseRecorder {
	t.Helper()
	rev := ""
	for _, w := range currentState(t, srv).Workflows {
		if w.Name == name {
			rev = w.Rev
		}
	}
	return post(srv, "/api/cases", "", `{"workflow": "`+name+`", "rev": "`+rev+`", `+what+`}`)
}

func TestTestIsDraftedFromARun(t *testing.T) {
	srv, _ := editUrgent(t)
	var run Run
	json.Unmarshal(post(srv, "/tickets?wait=true", "", ticket).Body.Bytes(), &run)
	if run.Status != "ok" {
		t.Fatalf("run = %+v", run)
	}

	rec := casesOf(t, srv, "triage-ticket", `"draft": "`+run.ID+`"`)
	var answer struct{ Draft draftView }
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if got := mustJSON(answer.Draft.Fixture); got != `{"body":{"customer":"ops@example.org","subject":"Dashboard returns 502"}}` {
		t.Errorf("fixture = %s", got)
	}
	// What a model decided is worth holding it to; what it wrote is not, and
	// an http step is not sent in a test, so nothing is expected of it but
	// that it is called.
	want := `[{"key":"classify","want":"called","keep":true},` +
		`{"key":"classify.category.value","want":"outage","keep":true},` +
		`{"key":"classify.priority.value","want":"high","keep":true},` +
		`{"key":"summarize","want":"called","keep":true},` +
		`{"key":"summarize.summary","want":"Something is wrong.","keep":false},` +
		`{"key":"alert_billing","want":"skipped","keep":true},` +
		`{"key":"page_oncall","want":"called","keep":true}]`
	if got := mustJSON(answer.Draft.Expect); got != want {
		t.Errorf("expectations:\n got %s\nwant %s", got, want)
	}

	// A run that did not succeed, or is not this workflow's, is not drafted from.
	for id, status := range map[string]int{"r-missing": http.StatusNotFound} {
		if rec := casesOf(t, srv, "triage-ticket", `"draft": "`+id+`"`); rec.Code != status {
			t.Errorf("draft from %s: status = %d", id, rec.Code)
		}
	}
	srv.opts.Model = offline{}
	var failed Run
	json.Unmarshal(post(srv, "/tickets?wait=true", "", ticket).Body.Bytes(), &failed)
	if rec := casesOf(t, srv, "triage-ticket", `"draft": "`+failed.ID+`"`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Only a run that succeeded") {
		t.Errorf("draft from a failed run: status = %d, body %s", rec.Code, rec.Body)
	}
}

func TestTestIsAddedAndRemoved(t *testing.T) {
	srv, dir := editUrgent(t)
	file := filepath.Join(dir, "workflow.yaml")
	before := readFile(t, file)

	add := `"add": {"name": "Dashboard down: pages on-call!", "fixture": {"body": {"subject": "Dashboard returns 502", "count": 3}},
		"expect": [{"key": "classify.category.value", "want": "outage"}, {"key": "page_oncall", "want": "called"},
		           {"key": "classify.priority.value == \"high\"", "want": true}, {"key": "trigger.body.count", "want": 3}]}`
	rec := casesOf(t, srv, "triage-ticket", add)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	added := `
  - name: 'Dashboard down: pages on-call!'
    fixture: fixtures/dashboard-down-pages-on-call.json
    expect:
      classify.category.value: outage
      page_oncall: called
      classify.priority.value == "high": true
      trigger.body.count: 3
`
	if got, want := readFile(t, file), before+added; got != want {
		t.Fatalf("file after adding the test:\n%s\nwant it to end:\n%s", got, added)
	}
	fixture := filepath.Join(dir, "fixtures", "dashboard-down-pages-on-call.json")
	if got, want := readFile(t, fixture), "{\n  \"body\": {\n    \"count\": 3,\n    \"subject\": \"Dashboard returns 502\"\n  }\n}\n"; got != want {
		t.Errorf("fixture = %q", got)
	}
	// The page is told of it, and it runs and passes like the others.
	cases := currentState(t, srv).Workflows[0].Cases
	if len(cases) != 4 || cases[3].Name != "Dashboard down: pages on-call!" || len(cases[3].Expect) != 4 {
		t.Fatalf("cases = %+v", cases)
	}
	_, runs := startTests(t, srv, `, "test": 3`)
	var run Run
	json.Unmarshal(get(srv, "/api/runs/"+runs[0].Run).Body.Bytes(), &run)
	if run.Status != "ok" || len(run.Checks) != 4 {
		t.Errorf("the new test's run = %+v", run)
	}

	// A test of the same name gets a fixture of its own.
	if rec := casesOf(t, srv, "triage-ticket", add); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(dir, "fixtures", "dashboard-down-pages-on-call-2.json")); err != nil {
		t.Errorf("the second fixture: %v", err)
	}

	// Removing both leaves the workflow file as it was. The fixtures stay.
	for range 2 {
		if rec := casesOf(t, srv, "triage-ticket", `"remove": 3`); rec.Code != http.StatusOK {
			t.Fatalf("removing: status = %d, body %s", rec.Code, rec.Body)
		}
	}
	if got := readFile(t, file); got != before {
		t.Errorf("file after removing the tests again:\n%s", got)
	}
	if _, err := os.Stat(fixture); err != nil {
		t.Errorf("the fixture was removed with the test: %v", err)
	}
}

func TestTestThatIsNotOneIsRefusedAndLeavesNothingBehind(t *testing.T) {
	srv, dir := editUrgent(t)
	file := filepath.Join(dir, "workflow.yaml")
	before := readFile(t, file)
	fixtures := func() int {
		entries, _ := os.ReadDir(filepath.Join(dir, "fixtures"))
		return len(entries)
	}
	had := fixtures()
	body := `"fixture": {"body": {"subject": "x"}}`

	for _, tt := range []struct {
		name   string
		what   string
		status int
		want   string
	}{
		{"no name", `"add": {"name": " ", ` + body + `, "expect": [{"key": "classify", "want": "called"}]}`,
			http.StatusUnprocessableEntity, "needs a name"},
		{"nothing to start with", `"add": {"name": "x", "expect": [{"key": "classify", "want": "called"}]}`,
			http.StatusUnprocessableEntity, "needs the event"},
		{"nothing expected", `"add": {"name": "x", ` + body + `, "expect": []}`,
			http.StatusUnprocessableEntity, "at least one thing to expect"},
		{"an expression that does not parse", `"add": {"name": "x", ` + body + `, "expect": [{"key": "classify.category.value ==", "want": true}]}`,
			http.StatusUnprocessableEntity, "Syntax error"},
		{"a field that is not there", `"add": {"name": "x", ` + body + `, "expect": [{"key": "classify.categry.value", "want": "outage"}]}`,
			http.StatusUnprocessableEntity, "classify has no categry"},
		{"a step expected to be something else", `"add": {"name": "x", ` + body + `, "expect": [{"key": "classify", "want": "maybe"}]}`,
			http.StatusUnprocessableEntity, "must be called or skipped"},
		{"the same thing expected twice", `"add": {"name": "x", ` + body + `, "expect": [{"key": "classify", "want": "called"}, {"key": " classify ", "want": "skipped"}]}`,
			http.StatusUnprocessableEntity, "is expected twice"},
		{"something expected that is not a single value", `"add": {"name": "x", ` + body + `, "expect": [{"key": "trigger.body", "want": {"subject": "x"}}]}`,
			http.StatusUnprocessableEntity, "must be text, a number, or true or false"},
		{"no such test to remove", `"remove": 7`, http.StatusUnprocessableEntity, "no test at position 7"},
		{"neither", `"x": 1`, http.StatusBadRequest, "drafts a test, adds one or removes one"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := casesOf(t, srv, "triage-ticket", tt.what)
			if rec.Code != tt.status || !strings.Contains(rec.Body.String(), tt.want) {
				t.Errorf("status = %d, body %s; want %d and %q", rec.Code, rec.Body, tt.status, tt.want)
			}
			if readFile(t, file) != before || fixtures() != had {
				t.Errorf("something was left behind: %d fixtures, had %d", fixtures(), had)
			}
		})
	}

	// A stale request is refused before anything is written.
	stale := `{"workflow": "triage-ticket", "rev": "0000000000000000", "add": {"name": "x", ` + body + `, "expect": [{"key": "classify", "want": "called"}]}}`
	if rec := post(srv, "/api/cases", "", stale); rec.Code != http.StatusConflict || fixtures() != had {
		t.Errorf("a stale request: status = %d, %d fixtures", rec.Code, fixtures())
	}
	// Neither a component nor a server that is not for editing takes tests.
	id := filepath.ToSlash(filepath.Join(dir, "components", "notify"))
	if rec := casesOf(t, srv, id, `"remove": 0`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "written in its file") {
		t.Errorf("a component: status = %d, body %s", rec.Code, rec.Body)
	}
	if rec := post(serveExample(t, "low", "").srv, "/api/cases", "", stale); rec.Code != http.StatusNotFound {
		t.Errorf("on a read-only server: status = %d", rec.Code)
	}
}

func TestTestIsHeldToWhatTheWorkflowTakes(t *testing.T) {
	srv, file := editEndpoint(t)
	dir := filepath.Dir(file)
	rec := casesOf(t, srv, "moderate-comment", `"add": {"name": "no text", "fixture": {"body": {"author": "dana"}}, "expect": [{"key": "check", "want": "called"}]}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "does not match trigger.input") {
		t.Errorf("status = %d, body %s", rec.Code, rec.Body)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "fixtures")); len(entries) != 2 {
		t.Errorf("the folder holds %d fixtures, want the 2 it started with", len(entries))
	}
}

func TestFirstTestOfAWorkflowIsAdded(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "workflow.yaml")
	const bare = "name: bare\ntrigger: {webhook: /bare}\nsteps:\n  - id: ping\n    http: {method: GET, url: \"http://unused.invalid\"}\n"
	os.WriteFile(file, []byte(bare), 0o644)
	w, err := workflow.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New([]*workflow.Workflow{w}, Options{Edit: true})
	if err != nil {
		t.Fatal(err)
	}
	rec := casesOf(t, srv, "bare", `"add": {"name": "pings", "fixture": {"body": {}}, "expect": [{"key": "ping", "want": "called"}, {"key": "ping.status", "want": 200}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	want := bare + "\ntests:\n  - name: pings\n    fixture: fixtures/pings.json\n    expect:\n      ping: called\n      ping.status: 200\n"
	if got := readFile(t, file); got != want {
		t.Errorf("file = %q, want %q", got, want)
	}
	// Removing the only test takes the list along.
	if rec := casesOf(t, srv, "bare", `"remove": 0`); rec.Code != http.StatusOK || readFile(t, file) != bare {
		t.Errorf("removing it: status = %d, file %q", rec.Code, readFile(t, file))
	}
}

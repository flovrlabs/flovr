package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/simonyosb/flowmaker/internal/workflow"
)

// named returns the description of what the page knows by name.
func named(t *testing.T, srv *Server, name string) workflowView {
	t.Helper()
	for _, w := range currentState(t, srv).Workflows {
		if w.Name == name {
			return w
		}
	}
	t.Fatalf("the page is not told of %s", name)
	return workflowView{}
}

func TestPageIsToldOfComponentsInTheFormOfWorkflows(t *testing.T) {
	srv, dir := editExample(t)
	notifyID := filepath.ToSlash(filepath.Join(dir, "components", "notify"))
	replyID := filepath.ToSlash(filepath.Join(dir, "components", "reply"))

	// One that does a single kind of work has that as its only step.
	notify := named(t, srv, notifyID)
	if !notify.Component || notify.Title != "notify" || !notify.Fixed || notify.Rev == "" || notify.Tests != 1 || len(notify.Cases) != 1 ||
		notify.Description == "" || mustJSON(notify.Inputs) != `[{"name":"url","type":"string"},{"name":"summary","type":"string"},{"name":"category","type":"string"}]` {
		t.Errorf("notify = %+v", notify)
	}
	if got := mustJSON(notify.UsedBy); got != `[{"workflow":"triage-ticket","step":"alert_billing"},{"workflow":"triage-ticket","step":"page_oncall"}]` {
		t.Errorf("notify is used by %s", got)
	}
	if len(notify.Steps) != 1 || notify.Steps[0].ID != "notify" || notify.Steps[0].Kind != "http" || mustJSON(notify.Steps[0].Reads) != `["input"]` {
		t.Fatalf("steps of notify = %+v", notify.Steps)
	}
	var fields []string
	for _, f := range notify.Steps[0].Edit {
		fields = append(fields, f.Label+" "+mustJSON(f.Path))
	}
	want := []string{`Method ["http","method"]`, `URL ["http","url"]`, `category ["http","body","category"]`, `summary ["http","body","summary"]`}
	if strings.Join(fields, "\n") != strings.Join(want, "\n") {
		t.Errorf("what can be changed in notify:\n%s", strings.Join(fields, "\n"))
	}

	// One made of steps has them, and what it gives back.
	reply := named(t, srv, replyID)
	if len(reply.Steps) != 2 || reply.Steps[0].ID != "draft" || reply.Steps[1].ID != "send" || mustJSON(reply.Steps[1].Reads) != `["input","draft"]` || reply.UsedBy != nil {
		t.Fatalf("reply = %+v", reply)
	}
	if reply.Steps[0].Edit[0].Label != "Run only when" || mustJSON(reply.Steps[0].Edit[1].Path) != `["steps",0,"ai","model"]` {
		t.Errorf("what can be changed in draft = %+v", reply.Steps[0].Edit)
	}
	if got := mustJSON(reply.Gives); got != `{"body":{"subject":"${draft.subject}"},"fields":["subject"],"reads":["draft"],"edit":[{"label":"subject","group":"Output","path":["output","subject"],"value":"${draft.subject}","kind":"text"}]}` {
		t.Errorf("what reply gives back = %s", got)
	}
	// A step that uses a component says which, by the name the page knows.
	if step := named(t, srv, "triage-ticket").Steps[2]; step.Target != notifyID {
		t.Errorf("the step's component = %q, want %q", step.Target, notifyID)
	}

	// A server that does not allow editing shows the components its
	// workflows use, and says nothing of what could be changed.
	plain := currentState(t, serveExample(t, "low", "").srv).Workflows
	if len(plain) != 2 || plain[1].Title != "notify" || plain[1].Rev != "" || plain[1].Cases != nil || plain[1].Steps[0].Edit != nil {
		t.Errorf("a read-only server tells of %+v", plain[1:])
	}
}

func TestComponentIsChangedFromThePage(t *testing.T) {
	srv, dir := editExample(t)
	id := filepath.ToSlash(filepath.Join(dir, "components", "notify"))
	file := filepath.Join(dir, "components", "notify", "component.yaml")
	before := readFile(t, file)
	save := func(rev, node string, edit map[string]any) *httptest.ResponseRecorder {
		edit["node"] = node
		body, _ := json.Marshal(map[string]any{"workflow": id, "rev": rev, "edits": []any{edit}})
		return post(srv, "/api/edit", "", string(body))
	}
	rev := named(t, srv, id).Rev

	rec := save(rev, "notify", set("PUT", "http", "method"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if got, want := readFile(t, file), strings.Replace(before, "  method: POST\n", "  method: PUT\n", 1); got != want {
		t.Errorf("file after the change:\n%s", got)
	}
	// Every step that uses the component is served with the change at once.
	for _, step := range named(t, srv, "triage-ticket").Steps[2:] {
		if step.Fields[1] != [2]string{"request", "PUT ${input.url}"} {
			t.Errorf("%s is served with %v", step.ID, step.Fields[1])
		}
	}
	var answer struct{ Workflow workflowView }
	json.Unmarshal(rec.Body.Bytes(), &answer)
	fresh := named(t, srv, id)
	if answer.Workflow.Rev == rev || answer.Workflow.Rev != fresh.Rev || len(answer.Workflow.UsedBy) != 2 {
		t.Errorf("the answer describes %+v", answer.Workflow)
	}

	// What is refused leaves the file as it is.
	after := readFile(t, file)
	for _, tt := range []struct {
		name   string
		rev    string
		node   string
		edit   map[string]any
		status int
		want   string
	}{
		{"the component would no longer be valid", fresh.Rev, "notify", set("${input.address}", "http", "url"),
			http.StatusUnprocessableEntity, "reads input.address, which is not listed under input"},
		{"the server could no longer serve what uses it", fresh.Rev, "notify", set("${input.url}/${env.NOWHERE}", "http", "url"),
			http.StatusUnprocessableEntity, "NOWHERE"},
		{"a value the page was not offered", fresh.Rev, "notify", set("other", "name"),
			http.StatusUnprocessableEntity, "cannot change this value"},
		{"a value meant for something else", fresh.Rev, "output", set("GET", "http", "method"),
			http.StatusConflict, "steps of the file changed"},
		{"a file that changed since the page read it", rev, "notify", set("GET", "http", "method"),
			http.StatusConflict, "changed after the page read it"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := save(tt.rev, tt.node, tt.edit)
			if rec.Code != tt.status || !strings.Contains(rec.Body.String(), tt.want) {
				t.Errorf("status = %d, body %s; want %d and %q", rec.Code, rec.Body, tt.status, tt.want)
			}
			if readFile(t, file) != after {
				t.Error("the file was changed")
			}
		})
	}

	// Steps are not added to or removed from a component by the page.
	body := `{"workflow": "` + id + `", "rev": "` + fresh.Rev + `", "remove": "notify"}`
	if rec := post(srv, "/api/steps", "", body); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "in its file") {
		t.Errorf("removing a step of a component: status = %d, body %s", rec.Code, rec.Body)
	}
}

func TestChangeToAComponentThatWouldBreakAWorkflowIsRefused(t *testing.T) {
	srv, dir := editExample(t)
	replyDir := filepath.Join(dir, "components", "reply")
	id := filepath.ToSlash(replyDir)
	// The workflow comes to use reply and to read what it gives back.
	if rec := steps(t, srv, `"add": {"id": "answer", "use": "components/reply", "with": {"ticket": "${trigger.body}", "url": "${env.PAGER_URL}"}}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	file := filepath.Join(replyDir, "component.yaml")
	before := readFile(t, file)

	// Given back as text of its own, the subject is still there to read.
	save := func(value string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"workflow": id, "rev": named(t, srv, id).Rev,
			"edits": []any{map[string]any{"path": []any{"output", "subject"}, "value": value, "node": "output"}}})
		return post(srv, "/api/edit", "", string(body))
	}
	if rec := save("About: ${draft.subject}"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if got := readFile(t, file); !strings.Contains(got, "  subject: 'About: ${draft.subject}'\n") {
		t.Errorf("file after the change:\n%s", got)
	}
	if rec := save("${draft.sbject}"); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "draft has no sbject") {
		t.Errorf("a field that is not there: status = %d, body %s", rec.Code, rec.Body)
	}
	if got := readFile(t, file); got == before || strings.Contains(got, "sbject") {
		t.Errorf("file after the refused change:\n%s", got)
	}
}

func TestPromptOfAComponentIsReadAndSaved(t *testing.T) {
	srv, dir := editExample(t)
	id := filepath.ToSlash(filepath.Join(dir, "components", "reply"))
	file := filepath.Join(dir, "components", "reply", "prompt.md")

	var prompt promptView
	rec := get(srv, "/api/prompt?workflow="+id+"&step=draft")
	if err := json.Unmarshal(rec.Body.Bytes(), &prompt); err != nil || rec.Code != http.StatusOK || prompt.File != "prompt.md" || prompt.Text != readFile(t, file) {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	body, _ := json.Marshal(promptRequest{Workflow: id, Step: "draft", Rev: prompt.Rev, Text: "Write a short reply."})
	if rec := post(srv, "/api/prompt", "", string(body)); rec.Code != http.StatusOK || readFile(t, file) != "Write a short reply.\n" {
		t.Errorf("saving: status = %d, body %s, file %q", rec.Code, rec.Body, readFile(t, file))
	}
	if rec := get(srv, "/api/prompt?workflow="+id+"&step=send"); rec.Code != http.StatusNotFound {
		t.Errorf("the prompt of a step that has none: status = %d", rec.Code)
	}
}

func TestTestsOfAComponentRunFromThePage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "triage-ticket")
	if err := os.CopyFS(dir, os.DirFS("../../examples/triage-ticket")); err != nil {
		t.Fatal(err)
	}
	w, err := workflow.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New([]*workflow.Workflow{w}, Options{
		Model: replier{}, Edit: true,
		Env: map[string]string{"PAGER_URL": "http://unused/pager", "BILLING_URL": "http://unused/billing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	start := func(id string) Run {
		t.Helper()
		rec := post(srv, "/api/test", "", `{"workflow": "`+id+`"}`)
		var answer struct{ Runs []started }
		if json.Unmarshal(rec.Body.Bytes(), &answer); rec.Code != http.StatusAccepted || len(answer.Runs) != 1 {
			t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
		}
		srv.Wait()
		var run Run
		json.Unmarshal(get(srv, "/api/runs/"+answer.Runs[0].Run).Body.Bytes(), &run)
		return run
	}

	// One that does a single kind of work has one step in its run.
	notifyID := filepath.ToSlash(filepath.Join(dir, "components", "notify"))
	run := start(notifyID)
	if run.Status != "ok" || run.Workflow != notifyID || run.Test != "takes a summary and a category" || len(run.Steps) != 1 ||
		run.Steps[0].ID != "notify" || run.Steps[0].Detail != "POST, not sent" || mustJSON(run.Checks) != `[{"key":"output.status","want":200}]` {
		t.Errorf("notify's test = %+v", run)
	}
	if input, _ := run.Trigger.(map[string]any); input["input"] == nil {
		t.Errorf("what the test gave the component = %v", run.Trigger)
	}

	// One made of steps has its steps, and what it gave back.
	replyID := filepath.ToSlash(filepath.Join(dir, "components", "reply"))
	run = start(replyID)
	if run.Status != "ok" || len(run.Steps) != 2 || run.Steps[0].ID != "draft" || run.Steps[1].ID != "send" || mustJSON(run.Gave) != `{"subject":"Re: Dashboard"}` {
		t.Errorf("reply's test = %+v", run)
	}
	for _, c := range run.Checks {
		if c.Failure != "" {
			t.Errorf("a check of reply's test failed: %+v", c)
		}
	}
	// The list of runs leaves out what it gave back.
	if listed := currentState(t, srv).Runs[0]; listed.ID != run.ID || listed.Gave != nil || listed.Workflow != replyID {
		t.Errorf("listed = %+v", listed)
	}
}

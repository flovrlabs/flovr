package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/simonyosb/flowmaker/internal/workflow"
)

// editFolder serves a copy of the examples folder for editing, as a folder
// that new workflows can be made in. It returns the folder and what the
// http steps send to.
func editFolder(t *testing.T) (*Server, string, *calls) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "examples")
	if err := os.CopyFS(root, os.DirFS("../../examples")); err != nil {
		t.Fatal(err)
	}
	workflows, err := workflow.Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	seen := &calls{}
	sink := httptest.NewServer(seen)
	t.Cleanup(sink.Close)
	srv, err := New(workflows, Options{
		Model: fakeModel{"high"}, HTTP: sink.Client(), Edit: true, Roots: []string{root},
		Env: map[string]string{"PAGER_URL": sink.URL + "/pager", "BILLING_URL": sink.URL + "/billing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, root, seen
}

// notifyIn is the name the page knows the notify component of root by.
func notifyIn(root string) string {
	return filepath.ToSlash(filepath.Join(root, "triage-ticket", "components", "notify"))
}

// refundAlerts asks for a workflow that tells billing of what it is sent.
func refundAlerts(root string) string {
	return `{"name": "refund-alerts", "trigger": {"webhook": "/refunds"}, "step": {
		"id": "tell", "use": "` + notifyIn(root) + `",
		"with": {"url": "${env.BILLING_URL}", "summary": "${trigger.body.text}", "category": "refund"}}}`
}

func TestWorkflowIsMadeFromThePage(t *testing.T) {
	srv, root, seen := editFolder(t)
	if roots := currentState(t, srv).Roots; len(roots) != 1 || roots[0] != filepath.ToSlash(root) {
		t.Fatalf("roots = %v", roots)
	}

	rec := post(srv, "/api/workflows", "", refundAlerts(root))
	var answer struct{ Workflow workflowView }
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	made := answer.Workflow
	if made.Name != "refund-alerts" || made.Trigger != "POST /refunds" || made.Rev == "" || len(made.Steps) != 1 || made.Steps[0].Component != "notify" {
		t.Errorf("answer = %+v", made)
	}

	dir := filepath.Join(root, "refund-alerts")
	want := `name: refund-alerts

trigger:
  webhook: /refunds

steps:
  - id: tell
    use: ../triage-ticket/components/notify
    with:
      url: ${env.BILLING_URL}
      summary: ${trigger.body.text}
      category: refund
`
	if got := readFile(t, filepath.Join(dir, "workflow.yaml")); got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
	// The file is one that the commands take as it is.
	if _, err := workflow.Load(dir); err != nil {
		t.Errorf("the file does not load: %v", err)
	}

	// The page lists it, and it is served at once.
	var names []string
	for _, w := range currentState(t, srv).Workflows {
		if !w.Component {
			names = append(names, w.Name)
		}
	}
	if got := strings.Join(names, " "); got != "moderate-comment triage-ticket refund-alerts" {
		t.Errorf("workflows = %s", got)
	}
	var run Run
	json.Unmarshal(post(srv, "/refunds?wait=true", "", `{"text": "Charged twice"}`).Body.Bytes(), &run)
	if run.Status != "ok" || len(seen.seen) != 1 || !strings.HasPrefix(seen.seen[0], "/billing ") || !strings.Contains(seen.seen[0], "Charged twice") {
		t.Errorf("run = %+v, sent %v", run, seen.seen)
	}

	// It is changed from the page like any other.
	body, _ := json.Marshal(map[string]any{
		"workflow": "refund-alerts", "rev": made.Rev,
		"edits": []any{set("billing", "steps", 0, "with", "category")},
	})
	if rec := post(srv, "/api/edit", "", string(body)); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if got := readFile(t, filepath.Join(dir, "workflow.yaml")); got != strings.Replace(want, "category: refund", "category: billing", 1) {
		t.Errorf("file after a change =\n%s", got)
	}
}

func TestChangesShowAWorkflowThatWasMade(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	srv, root, _ := editFolder(t)
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"commit", "-q", "-m", "start"}} {
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.name=t", "-c", "user.email=t@example.org", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if rec := post(srv, "/api/workflows", "", refundAlerts(root)); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}

	// A file git does not know is listed, but its text is shown only if it
	// is of a kind the page writes.
	if err := os.WriteFile(filepath.Join(root, "refund-alerts", ".env"), []byte("SECRET=do-not-show\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "refund-alerts", "notes.json"), []byte(`{"big": "`+strings.Repeat("x", maxShown)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Git does not know the files yet, and would show only their folder.
	var view changesView
	json.Unmarshal(get(srv, "/api/changes?workflow=refund-alerts").Body.Bytes(), &view)
	var listed []string
	for _, f := range view.Files {
		listed = append(listed, f.Change+" "+f.Path)
	}
	if got := strings.Join(listed, ", "); got != "new refund-alerts/.env, new refund-alerts/notes.json, new refund-alerts/workflow.yaml" {
		t.Errorf("files = %s", got)
	}
	if strings.Contains(view.Diff, "do-not-show") || strings.Contains(view.Diff, "notes.json") {
		t.Errorf("the diff shows what it should only list:\n%s", view.Diff)
	}
	for _, want := range []string{"+++ b/refund-alerts/workflow.yaml\n", "\n+name: refund-alerts\n", "\n+    use: ../triage-ticket/components/notify\n"} {
		if !strings.Contains(view.Diff, want) {
			t.Errorf("the diff lacks %q:\n%s", want, view.Diff)
		}
	}
	// What git does know is shown as before.
	json.Unmarshal(get(srv, "/api/changes?workflow=triage-ticket").Body.Bytes(), &view)
	if len(view.Files) != 0 || view.Diff != "" {
		t.Errorf("triage-ticket, which did not change: %+v", view)
	}
}

func TestWorkflowOnAScheduleIsMade(t *testing.T) {
	srv, root, _ := editFolder(t)
	rec := post(srv, "/api/workflows", "", `{"name": "nightly_report", "root": "`+filepath.ToSlash(root)+`",
		"trigger": {"cron": "*/5 3 * * *"}, "step": {"id": "tell", "when": "true", "use": "`+notifyIn(root)+`",
		"with": {"url": "${env.BILLING_URL}", "summary": "It is ${trigger.time}", "category": "report"}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	w, err := workflow.Load(filepath.Join(root, "nightly_report"))
	if err != nil {
		t.Fatal(err)
	}
	if w.Trigger.Cron != "*/5 3 * * *" || w.Steps[0].When != "true" || w.Steps[0].With["summary"] != "It is ${trigger.time}" {
		t.Errorf("trigger = %+v, step = %+v", w.Trigger, w.Steps[0])
	}
	routes := strings.Join(srv.Routes(), "\n")
	if !strings.Contains(routes, "cron */5 3 * * *  ->  nightly_report") {
		t.Errorf("routes =\n%s", routes)
	}
}

func TestNothingIsMadeOfAWorkflowThatCouldNotBeServed(t *testing.T) {
	srv, root, _ := editFolder(t)
	if err := os.Mkdir(filepath.Join(root, "taken"), 0o755); err != nil {
		t.Fatal(err)
	}
	notify := notifyIn(root)
	with := `"with": {"url": "${env.BILLING_URL}", "summary": "${trigger.body.text}", "category": "refund"}`
	step := `"step": {"id": "tell", "use": "` + notify + `", ` + with + `}`
	hook := `"trigger": {"webhook": "/refunds"}`
	before, _ := os.ReadDir(root)

	for _, tc := range []struct{ name, body, want string }{
		{"no name", `{"name": " ", ` + hook + `, ` + step + `}`, "needs a name"},
		{"a name that is a path", `{"name": "a/b", ` + hook + `, ` + step + `}`, "only letters, digits"},
		{"a name that leaves the folder", `{"name": "..", ` + hook + `, ` + step + `}`, "only letters, digits"},
		{"a name that is too long", `{"name": "` + strings.Repeat("n", 61) + `", ` + hook + `, ` + step + `}`, "at most 60 characters"},
		{"a name that is passed over", `{"name": "testdata", ` + hook + `, ` + step + `}`, "passed over"},
		{"a name another workflow has", `{"name": "triage-ticket", ` + hook + `, ` + step + `}`, "already a workflow named triage-ticket"},
		{"a folder that is there", `{"name": "taken", ` + hook + `, ` + step + `}`, "already something at"},
		{"a folder the server was not started with", `{"name": "new", "root": "/tmp", ` + hook + `, ` + step + `}`, "one of the folders the server was started with"},
		{"no trigger", `{"name": "new", ` + step + `}`, "one thing that starts it"},
		{"two triggers", `{"name": "new", "trigger": {"webhook": "/refunds", "cron": "0 3 * * *"}, ` + step + `}`, "one thing that starts it"},
		{"a webhook path another workflow has", `{"name": "new", "trigger": {"webhook": "/tickets"}, ` + step + `}`, "both use the webhook path /tickets"},
		{"a webhook path the server uses", `{"name": "new", "trigger": {"webhook": "/api/new"}, ` + step + `}`, "used by the server itself"},
		{"a schedule that is not one", `{"name": "new", "trigger": {"cron": "every day"}, ` + step + `}`, "cron"},
		{"no component", `{"name": "new", ` + hook + `, "step": {"id": "tell"}}`, "is not one the page knows"},
		{"a component nobody knows", `{"name": "new", ` + hook + `, "step": {"id": "tell", "use": "components/notify", ` + with + `}}`, "is not one the page knows"},
		{"a step id that is not one", `{"name": "new", ` + hook + `, "step": {"id": "1st", "use": "` + notify + `", ` + with + `}}`, "id may contain only"},
		{"input the component is not given", `{"name": "new", ` + hook + `, "step": {"id": "tell", "use": "` + notify + `", "with": {"url": "http://x"}}}`, "summary"},
		{"a variable that is not set", `{"name": "new", ` + hook + `, "step": {"id": "tell", "use": "` + notify + `",
			"with": {"url": "${env.NOWHERE}", "summary": "x", "category": "y"}}}`, "not set: NOWHERE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := post(srv, "/api/workflows", "", tc.body)
			var f failure
			json.Unmarshal(rec.Body.Bytes(), &f)
			said := f.Error
			for _, p := range f.Problems {
				said += "\n" + p.Message
				if p.Line != 0 {
					t.Errorf("a problem names line %d of a file that was never written", p.Line)
				}
			}
			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(said, tc.want) {
				t.Errorf("status = %d, said %q; want 422 and %q", rec.Code, said, tc.want)
			}
		})
	}

	after, _ := os.ReadDir(root)
	if len(after) != len(before) {
		t.Errorf("the folder holds %d things, had %d", len(after), len(before))
	}
	if inside, _ := os.ReadDir(filepath.Join(root, "taken")); len(inside) != 0 {
		t.Errorf("the folder that was there now holds %d things", len(inside))
	}
	if n := len(srv.live.Load().workflows); n != 2 {
		t.Errorf("%d workflows are served, want 2", n)
	}
}

func TestWorkflowIsMadeOnlyInAFolderTheServerWasStartedWith(t *testing.T) {
	// A server started with a workflow file has no such folder.
	srv, dir := editExample(t)
	if roots := currentState(t, srv).Roots; len(roots) != 0 {
		t.Errorf("roots = %v", roots)
	}
	rec := post(srv, "/api/workflows", "", refundAlerts(filepath.Dir(dir)))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "nowhere to make a new workflow") {
		t.Errorf("status = %d, body %s", rec.Code, rec.Body)
	}

	// A server that is not for editing makes nothing.
	f := serveExample(t, "low", "")
	if rec := post(f.srv, "/api/workflows", "", refundAlerts("examples")); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, body %s", rec.Code, rec.Body)
	}
}

func TestStepIsAddedFromAComponentOfAnotherFolder(t *testing.T) {
	srv, root, _ := editFolder(t)
	offered := map[string]string{}
	revs := map[string]string{}
	for _, w := range currentState(t, srv).Workflows {
		var uses []string
		for _, c := range w.Components {
			uses = append(uses, c.Use)
		}
		offered[w.Name], revs[w.Name] = strings.Join(uses, " "), w.Rev
	}
	// A workflow's own components come first, named from its folder.
	if got := offered["triage-ticket"]; got != "components/notify components/reply" {
		t.Errorf("triage-ticket is offered %q", got)
	}
	if got := offered["moderate-comment"]; got != "../triage-ticket/components/notify ../triage-ticket/components/reply" {
		t.Errorf("moderate-comment is offered %q", got)
	}

	rec := post(srv, "/api/steps", "", `{"workflow": "moderate-comment", "rev": "`+revs["moderate-comment"]+`", "add": {
		"id": "tell", "when": "!check.allowed.value", "use": "../triage-ticket/components/notify",
		"with": {"url": "${env.BILLING_URL}", "summary": "${trigger.body.text}", "category": "${check.reason.value}"}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	file := readFile(t, filepath.Join(root, "moderate-comment", "workflow.yaml"))
	if !strings.Contains(file, "    use: ../triage-ticket/components/notify\n    with:\n      url: ${env.BILLING_URL}\n") {
		t.Errorf("file =\n%s", file)
	}
	if _, err := workflow.Load(filepath.Join(root, "moderate-comment")); err != nil {
		t.Errorf("the file does not load: %v", err)
	}
}

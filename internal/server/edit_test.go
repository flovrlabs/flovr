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
	"time"

	"github.com/simonyosb/flowmaker/internal/workflow"
)

// editExample serves a copy of the triage-ticket example that the page may
// change, and returns the copy's folder.
func editExample(t *testing.T) (*Server, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "triage-ticket")
	if err := os.CopyFS(dir, os.DirFS("../../examples/triage-ticket")); err != nil {
		t.Fatal(err)
	}
	w, err := workflow.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	// No ticket is urgent to this model, so no http step is sent.
	srv, err := New([]*workflow.Workflow{w}, Options{
		Model: fakeModel{"low"},
		Env:   map[string]string{"PAGER_URL": "http://unused/pager", "BILLING_URL": "http://unused/billing"},
		Edit:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, dir
}

func currentState(t *testing.T, srv *Server) stateView {
	t.Helper()
	var state stateView
	if err := json.Unmarshal(get(srv, "/api/state").Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	return state
}

// change posts edits for the example as the page would.
func change(srv *Server, rev string, edits ...any) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"workflow": "triage-ticket", "rev": rev, "edits": edits})
	return post(srv, "/api/edit", "", string(body))
}

func set(value string, path ...any) map[string]any {
	return map[string]any{"path": path, "value": value}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPageIsToldWhatItMayChange(t *testing.T) {
	srv, _ := editExample(t)
	state := currentState(t, srv)
	// The workflow comes first; the components in its folder follow it.
	if !state.Editable || len(state.Workflows) != 3 || state.Workflows[0].Rev == "" || state.Workflows[0].Component || !state.Workflows[1].Component {
		t.Fatalf("state = %+v", state)
	}
	w := state.Workflows[0]
	if len(w.Edit) != 2 || mustJSON(w.Edit[0]) != `{"label":"Webhook path","path":["trigger","webhook"],"value":"/tickets","kind":"text"}` {
		t.Errorf("trigger fields = %s", mustJSON(w.Edit))
	}
	// The example names no schema for its body, so one can be given.
	if schema := w.Edit[1]; schema.Label != "Body schema" || schema.Value != "" || !schema.Optional || schema.Hint == "" || mustJSON(schema.Path) != `["trigger","input"]` {
		t.Errorf("body schema field = %+v", schema)
	}

	var labels []string
	for _, step := range w.Steps {
		for _, f := range step.Edit {
			label := step.ID + ": " + f.Label
			if f.Group != "" {
				label = step.ID + ": " + f.Group + " / " + f.Label
			}
			labels = append(labels, label+" ("+f.Kind+")")
		}
	}
	want := []string{
		"classify: Run only when (expression)", "classify: Model (text)", "classify: Input (text)",
		"classify: category / Question (text)",
		"classify: category / refund (text)", "classify: category / billing (text)", "classify: category / outage (text)",
		"classify: category / bug (text)", "classify: category / account (text)", "classify: category / other (text)",
		"classify: priority / Question (text)",
		"classify: priority / low (text)", "classify: priority / medium (text)", "classify: priority / high (text)",
		"summarize: Run only when (expression)", "summarize: Model (text)", "summarize: Prompt (prompt)",
		"summarize: Input (text)", "summarize: Output schema (text)",
		// These two use a component: what they give it can be changed, in the
		// order the component lists its inputs.
		"alert_billing: Run only when (expression)",
		"alert_billing: Input / url (text)", "alert_billing: Input / summary (text)", "alert_billing: Input / category (text)",
		"page_oncall: Run only when (expression)",
		"page_oncall: Input / url (text)", "page_oncall: Input / summary (text)", "page_oncall: Input / category (text)",
	}
	if strings.Join(labels, "\n") != strings.Join(want, "\n") {
		t.Errorf("fields:\n%s\nwant:\n%s", strings.Join(labels, "\n"), strings.Join(want, "\n"))
	}
	// A score's answers are a list in the file, a choice's a mapping.
	high := w.Steps[0].Edit[13]
	if got := mustJSON(high.Path); got != `["steps",0,"decide","questions","priority","score",2,"high"]` {
		t.Errorf("path of a score's answer = %s", got)
	}

	// A server that does not allow editing says nothing of all this.
	f := serveExample(t, "low", "")
	plain := currentState(t, f.srv)
	if plain.Editable || plain.Workflows[0].Rev != "" || plain.Workflows[0].Edit != nil || plain.Workflows[0].Steps[0].Edit != nil {
		t.Errorf("a read-only server offers editing: %+v", plain.Workflows[0])
	}
}

func TestEditChangesOnlyTheLinesOfTheValues(t *testing.T) {
	srv, dir := editExample(t)
	file := filepath.Join(dir, "workflow.yaml")
	before := readFile(t, file)
	rev := currentState(t, srv).Workflows[0].Rev

	rec := change(srv, rev,
		set(`classify.priority.value != "low"`, "steps", 1, "when"),
		set("They want their money back.", "steps", 0, "decide", "questions", "category", "choice", "refund"),
		set("trigger.body != null", "steps", 0, "when"),
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	after := readFile(t, file)
	replacer := strings.NewReplacer(
		"  - id: classify\n", "  - id: classify\n    when: trigger.body != null\n",
		"            refund: They want money back for a charge.\n", "            refund: They want their money back.\n",
		"  - id: summarize\n    when: classify.priority.value == \"high\"\n", "  - id: summarize\n    when: classify.priority.value != \"low\"\n",
	)
	if want := replacer.Replace(before); after != want {
		t.Errorf("file after the change:\n%s\nwant:\n%s", after, want)
	}

	// The server runs the changed workflow at once, and the answer carries
	// what the page needs for the next change.
	var answer struct{ Workflow workflowView }
	json.Unmarshal(rec.Body.Bytes(), &answer)
	state := currentState(t, srv)
	if answer.Workflow.Rev == rev || answer.Workflow.Rev != state.Workflows[0].Rev {
		t.Errorf("rev: was %s, answer %s, state %s", rev, answer.Workflow.Rev, state.Workflows[0].Rev)
	}
	if got := state.Workflows[0].Steps[1].When; got != `classify.priority.value != "low"` {
		t.Errorf("the server serves when = %s", got)
	}

	// Saving an optional value empty removes it.
	rec = change(srv, state.Workflows[0].Rev, set("  ", "steps", 0, "when"))
	if rec.Code != http.StatusOK || strings.Contains(readFile(t, file), "trigger.body != null") {
		t.Errorf("status = %d, body %s; file:\n%s", rec.Code, rec.Body, readFile(t, file))
	}
}

func TestEditIsRefusedWithoutTouchingTheFile(t *testing.T) {
	srv, dir := editExample(t)
	file := filepath.Join(dir, "workflow.yaml")
	before := readFile(t, file)
	rev := currentState(t, srv).Workflows[0].Rev

	tests := []struct {
		name   string
		rev    string
		edit   map[string]any
		status int
		want   string
	}{
		{"an expression that does not compile", rev, set("classify.priority.value ==", "steps", 1, "when"),
			http.StatusUnprocessableEntity, `step \"summarize\": when:`},
		{"a step that is not defined yet", rev, set("summarize.summary != ''", "steps", 0, "when"),
			http.StatusUnprocessableEntity, `step \"classify\": when:`},
		{"a required value left empty", rev, set("", "steps", 1, "ai", "model"),
			http.StatusUnprocessableEntity, "ai.model is required"},
		{"an environment variable that is not set", rev, set("${env.MISSING_URL}", "steps", 2, "with", "url"),
			http.StatusUnprocessableEntity, "MISSING_URL"},
		{"what a component does", rev, set("GET", "steps", 2, "http", "method"),
			http.StatusUnprocessableEntity, "cannot change this value"},
		{"a webhook path the server uses itself", rev, set("/api/state", "trigger", "webhook"),
			http.StatusUnprocessableEntity, "used by the server itself"},
		{"a value the page was not offered", rev, set("other", "steps", 0, "id"),
			http.StatusUnprocessableEntity, "cannot change this value"},
		{"a test", rev, set("skipped", "tests", 0, "expect", "summarize"),
			http.StatusUnprocessableEntity, "cannot change this value"},
		{"a file that changed since the page read it", "0000000000000000", set("x", "steps", 1, "when"),
			http.StatusConflict, "changed after the page read it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := change(srv, tt.rev, tt.edit)
			if rec.Code != tt.status || !strings.Contains(rec.Body.String(), tt.want) {
				t.Errorf("status = %d, body %s; want %d and %q", rec.Code, rec.Body, tt.status, tt.want)
			}
			if readFile(t, file) != before {
				t.Error("the file was changed")
			}
		})
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 5 {
		t.Errorf("the folder holds %d entries, want the 5 it started with", len(entries))
	}

	// A workflow of that name must exist.
	body := `{"workflow": "other", "rev": "` + rev + `", "edits": []}`
	if rec := post(srv, "/api/edit", "", body); rec.Code != http.StatusNotFound {
		t.Errorf("unknown workflow: status = %d", rec.Code)
	}
}

func TestChangesAreAcceptedOnlyFromTheServersOwnPage(t *testing.T) {
	srv, _ := editExample(t)
	rev := currentState(t, srv).Workflows[0].Rev
	body := `{"workflow": "triage-ticket", "rev": "` + rev + `", "edits": []}`

	send := func(header, value string) int {
		req := request(http.MethodPost, "/api/edit", body)
		if header != "" {
			req.Header.Set(header, value)
		}
		return do(srv, req).Code
	}
	for _, tt := range []struct {
		header, value string
		want          int
	}{
		{"", "", http.StatusOK},
		{"Origin", "http://127.0.0.1:8080", http.StatusOK},
		{"Sec-Fetch-Site", "same-origin", http.StatusOK},
		{"Origin", "http://evil.example", http.StatusForbidden},
		{"Origin", "http://127.0.0.1:9999", http.StatusForbidden},
		{"Sec-Fetch-Site", "cross-site", http.StatusForbidden},
		{"Content-Type", "text/plain", http.StatusUnsupportedMediaType},
	} {
		if got := send(tt.header, tt.value); got != tt.want {
			t.Errorf("%s %q: status = %d, want %d", tt.header, tt.value, got, tt.want)
		}
	}

	// A server not started for editing has none of this.
	f := serveExample(t, "low", "")
	for _, target := range []string{"/api/edit", "/api/prompt"} {
		if rec := post(f.srv, target, "", body); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s on a read-only server: status = %d", target, rec.Code)
		}
	}
	for _, target := range []string{"/api/prompt?workflow=triage-ticket&step=summarize", "/api/changes?workflow=triage-ticket"} {
		if rec := get(f.srv, target); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s on a read-only server: status = %d", target, rec.Code)
		}
	}
}

func TestEditNeedsTheToken(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "triage-ticket")
	if err := os.CopyFS(dir, os.DirFS("../../examples/triage-ticket")); err != nil {
		t.Fatal(err)
	}
	w, err := workflow.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New([]*workflow.Workflow{w}, Options{
		Env:   map[string]string{"PAGER_URL": "http://unused", "BILLING_URL": "http://unused"},
		Edit:  true,
		Token: "s3cret",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"workflow": "triage-ticket", "rev": "x", "edits": []}`
	if rec := post(srv, "/api/edit", "", body); rec.Code != http.StatusUnauthorized {
		t.Errorf("without the token: status = %d", rec.Code)
	}
	if rec := post(srv, "/api/edit", "s3cret", body); rec.Code != http.StatusConflict {
		t.Errorf("with the token: status = %d, want the request to get as far as the rev check", rec.Code)
	}
}

func TestPromptIsReadAndSaved(t *testing.T) {
	srv, dir := editExample(t)
	file := filepath.Join(dir, "prompts", "summarize.md")

	var prompt promptView
	rec := get(srv, "/api/prompt?workflow=triage-ticket&step=summarize")
	if err := json.Unmarshal(rec.Body.Bytes(), &prompt); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if prompt.File != "prompts/summarize.md" || prompt.Path != file || prompt.Text != readFile(t, file) || prompt.Rev == "" {
		t.Errorf("prompt = %+v", prompt)
	}

	save := func(rev, text string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(promptRequest{Workflow: "triage-ticket", Step: "summarize", Rev: rev, Text: text})
		return post(srv, "/api/prompt", "", string(body))
	}
	rec = save(prompt.Rev, "Summarize the ticket in one sentence.\r\nBe specific.\n\n\n")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if got, want := readFile(t, file), "Summarize the ticket in one sentence.\nBe specific.\n"; got != want {
		t.Errorf("file = %q, want %q", got, want)
	}
	var saved promptView
	json.Unmarshal(rec.Body.Bytes(), &saved)

	if rec := save(prompt.Rev, "Too late."); rec.Code != http.StatusConflict {
		t.Errorf("saving over a newer file: status = %d", rec.Code)
	}
	if rec := save(saved.Rev, " \n"); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("saving an empty prompt: status = %d", rec.Code)
	}
	if rec := get(srv, "/api/prompt?workflow=triage-ticket&step=classify"); rec.Code != http.StatusNotFound {
		t.Errorf("the prompt of a step that has none: status = %d", rec.Code)
	}
}

func TestPromptOutsideTheWorkflowFolderIsNotOpened(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "triage-ticket")
	if err := os.CopyFS(dir, os.DirFS("../../examples/triage-ticket")); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "shared.md"), []byte("A prompt kept elsewhere.\n"), 0o644)
	file := filepath.Join(dir, "workflow.yaml")
	os.WriteFile(file, []byte(strings.Replace(readFile(t, file), "prompts/summarize.md", "../shared.md", 1)), 0o644)

	w, err := workflow.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New([]*workflow.Workflow{w}, Options{
		Env:  map[string]string{"PAGER_URL": "http://unused", "BILLING_URL": "http://unused"},
		Edit: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec := get(srv, "/api/prompt?workflow=triage-ticket&step=summarize"); rec.Code != http.StatusForbidden {
		t.Errorf("reading: status = %d, body %s", rec.Code, rec.Body)
	}
	body, _ := json.Marshal(promptRequest{Workflow: "triage-ticket", Step: "summarize", Rev: "x", Text: "Changed."})
	if rec := post(srv, "/api/prompt", "", string(body)); rec.Code != http.StatusForbidden {
		t.Errorf("saving: status = %d, body %s", rec.Code, rec.Body)
	}
	if got := readFile(t, filepath.Join(root, "shared.md")); got != "A prompt kept elsewhere.\n" {
		t.Errorf("the file outside the folder was changed to %q", got)
	}
}

// rewrite changes the file by other means than the page, as an editor would.
func rewrite(t *testing.T, file, old, new string) {
	t.Helper()
	text := readFile(t, file)
	if !strings.Contains(text, old) {
		t.Fatalf("%s does not contain %q", file, old)
	}
	if err := os.WriteFile(file, []byte(strings.Replace(text, old, new, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	// Some file systems keep times too coarse to tell two quick writes apart.
	later := time.Now().Add(2 * time.Second)
	os.Chtimes(file, later, later)
}

func TestChangesToTheFileAreTakenUp(t *testing.T) {
	srv, dir := editExample(t)
	file := filepath.Join(dir, "workflow.yaml")
	first := currentState(t, srv).Workflows[0]

	rewrite(t, file, "webhook: /tickets", "webhook: /support")
	second := currentState(t, srv).Workflows[0]
	if second.Trigger != "POST /support" || second.Rev == first.Rev || second.Problem != "" {
		t.Fatalf("after the change: trigger %q, rev %s (was %s), problem %q", second.Trigger, second.Rev, first.Rev, second.Problem)
	}
	if rec := post(srv, "/support?wait=true", "", ticket); rec.Code != http.StatusOK {
		t.Errorf("POST to the new path: status = %d, body %s", rec.Code, rec.Body)
	}
	if rec := post(srv, "/tickets", "", ticket); rec.Code != http.StatusNotFound {
		t.Errorf("POST to the old path: status = %d", rec.Code)
	}

	// A file with a mistake keeps being served as it was, and the page is
	// told why and cannot change it.
	rewrite(t, file, "webhook: /support", "webhook: support")
	third := currentState(t, srv).Workflows[0]
	if third.Trigger != "POST /support" || third.Rev != second.Rev || !strings.Contains(third.Problem, "must be a path starting with /") {
		t.Fatalf("after the mistake: trigger %q, rev %s, problem %q", third.Trigger, third.Rev, third.Problem)
	}
	if rec := post(srv, "/support?wait=true", "", ticket); rec.Code != http.StatusOK {
		t.Errorf("POST while the file has a mistake: status = %d", rec.Code)
	}
	if rec := change(srv, third.Rev, set("x", "steps", 1, "when")); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "must be fixed in the file") {
		t.Errorf("a change while the file has a mistake: status = %d, body %s", rec.Code, rec.Body)
	}

	rewrite(t, file, "webhook: support", "webhook: /support")
	if fourth := currentState(t, srv).Workflows[0]; fourth.Problem != "" || fourth.Rev != second.Rev {
		t.Errorf("after the fix: rev %s, problem %q", fourth.Rev, fourth.Problem)
	}
}

func TestEditedScheduleReplacesTheOldOne(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "workflow.yaml")
	os.WriteFile(file, []byte("name: nightly\ntrigger:\n  cron: 0 3 * * *\nsteps:\n  - id: ping\n    http:\n      method: GET\n      url: http://unused/\n"), 0o644)
	w, err := workflow.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New([]*workflow.Workflow{w}, Options{Edit: true})
	if err != nil {
		t.Fatal(err)
	}
	stop := srv.Start()
	defer stop()

	state := currentState(t, srv)
	body, _ := json.Marshal(map[string]any{"workflow": "nightly", "rev": state.Workflows[0].Rev, "edits": []any{set("30 4 * * *", "trigger", "cron")}})
	if rec := post(srv, "/api/edit", "", string(body)); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if routes := strings.Join(srv.Routes(), "\n"); !strings.Contains(routes, "cron 30 4 * * *  ->  nightly") {
		t.Errorf("routes:\n%s", routes)
	}
	srv.mu.Lock()
	entries := srv.cron.Entries()
	srv.mu.Unlock()
	if len(entries) != 1 || entries[0].Next.Minute() != 30 || entries[0].Next.Hour() != 4 {
		t.Errorf("schedule = %+v, want one entry at 04:30", entries)
	}

	body, _ = json.Marshal(map[string]any{"workflow": "nightly", "rev": currentState(t, srv).Workflows[0].Rev, "edits": []any{set("every night", "trigger", "cron")}})
	if rec := post(srv, "/api/edit", "", string(body)); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "not a schedule of five fields") {
		t.Errorf("a schedule that is not one: status = %d, body %s", rec.Code, rec.Body)
	}

	// A value with a fixed set of choices takes only those.
	method := currentState(t, srv).Workflows[0].Steps[0].Edit[1]
	if method.Label != "Method" || method.Kind != kindChoice || method.Value != "GET" || len(method.Options) != 5 {
		t.Fatalf("the method field = %+v", method)
	}
	for value, want := range map[string]int{"FETCH": http.StatusUnprocessableEntity, "POST": http.StatusOK} {
		body, _ = json.Marshal(map[string]any{"workflow": "nightly", "rev": currentState(t, srv).Workflows[0].Rev, "edits": []any{set(value, "steps", 0, "http", "method")}})
		if rec := post(srv, "/api/edit", "", string(body)); rec.Code != want {
			t.Errorf("method %s: status = %d, body %s", value, rec.Code, rec.Body)
		}
	}
	if !strings.Contains(readFile(t, file), "      method: POST\n") {
		t.Errorf("the file after the change:\n%s", readFile(t, file))
	}
}

func TestChangesShowWhatGitSees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	srv, dir := editExample(t)

	var view changesView
	json.Unmarshal(get(srv, "/api/changes?workflow=triage-ticket").Body.Bytes(), &view)
	if view.Git {
		t.Fatalf("outside a repository: %+v", view)
	}

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.org", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("add", ".")
	run("commit", "-q", "-m", "start")
	json.Unmarshal(get(srv, "/api/changes?workflow=triage-ticket").Body.Bytes(), &view)
	if !view.Git || len(view.Files) != 0 || view.Diff != "" {
		t.Fatalf("with nothing changed: %+v", view)
	}

	rev := currentState(t, srv).Workflows[0].Rev
	if rec := change(srv, rev, set("openrouter/other/model", "steps", 1, "ai", "model")); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	json.Unmarshal(get(srv, "/api/changes?workflow=triage-ticket").Body.Bytes(), &view)
	if len(view.Files) != 1 || view.Files[0] != (changeView{Path: "workflow.yaml", Change: "modified"}) {
		t.Errorf("files = %+v", view.Files)
	}
	var added, removed []string
	for _, line := range strings.Split(view.Diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			added = append(added, line)
		case strings.HasPrefix(line, "-"):
			removed = append(removed, line)
		}
	}
	if len(added) != 1 || added[0] != "+      model: openrouter/other/model" || len(removed) != 1 {
		t.Errorf("diff:\n%s", view.Diff)
	}
}

func TestWhatAStepGivesAComponentCanBeChanged(t *testing.T) {
	srv, dir := editExample(t)
	state := currentState(t, srv)
	step := state.Workflows[0].Steps[3]
	if step.Component != "notify" || step.Uses != "components/notify" || step.Kind != "http" {
		t.Fatalf("step = %+v", step)
	}
	url := step.Edit[1]
	if url.Label != "url" || url.Group != "Input" || url.Value != "${env.PAGER_URL}" || mustJSON(url.Path) != `["steps",3,"with","url"]` {
		t.Fatalf("field = %+v", url)
	}

	rec := change(srv, state.Workflows[0].Rev, set("${env.BILLING_URL}", "steps", 3, "with", "url"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if got := readFile(t, filepath.Join(dir, "workflow.yaml")); strings.Count(got, "      url: ${env.BILLING_URL}\n") != 2 {
		t.Errorf("the file after the change:\n%s", got)
	}
	// The component file is not the page's to change.
	if got, want := readFile(t, filepath.Join(dir, "components", "notify", "component.yaml")), readFile(t, "../../examples/triage-ticket/components/notify/component.yaml"); got != want {
		t.Error("the component file was changed")
	}
}

func TestChangesToAComponentFileAreTakenUp(t *testing.T) {
	srv, dir := editExample(t)
	file := filepath.Join(dir, "components", "notify", "component.yaml")
	first := currentState(t, srv).Workflows[0]

	rewrite(t, file, "method: POST", "method: PUT")
	second := currentState(t, srv).Workflows[0]
	if got := second.Steps[2].Fields[1]; got != [2]string{"request", "PUT ${input.url}"} || second.Problem != "" {
		t.Fatalf("after the change: %v, problem %q", got, second.Problem)
	}
	// The workflow file itself is as it was, so the page can go on from it.
	if second.Rev != first.Rev {
		t.Errorf("rev changed from %s to %s", first.Rev, second.Rev)
	}

	// A component that no longer fits how it is used is a problem of the
	// workflow, which keeps being served as it was.
	rewrite(t, file, "  category: string\n", "")
	third := currentState(t, srv).Workflows[0]
	if !strings.Contains(third.Problem, "with.category is not an input of notify") && !strings.Contains(third.Problem, "reads input.category") {
		t.Fatalf("after the mistake: problem %q", third.Problem)
	}
	if got := third.Steps[2].Fields[1]; got != [2]string{"request", "PUT ${input.url}"} {
		t.Errorf("after the mistake the server serves %v", got)
	}

	rewrite(t, file, "  summary: string\n", "  summary: string\n  category: string\n")
	if fourth := currentState(t, srv).Workflows[0]; fourth.Problem != "" {
		t.Errorf("after the fix: problem %q", fourth.Problem)
	}
}

// editEndpoint serves a copy of the moderate-comment example that the page
// may change, and returns the copy's workflow file.
func editEndpoint(t *testing.T) (*Server, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "moderate-comment")
	if err := os.CopyFS(dir, os.DirFS("../../examples/moderate-comment")); err != nil {
		t.Fatal(err)
	}
	w, err := workflow.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New([]*workflow.Workflow{w}, Options{Model: moderator{allowed: true, reason: "none"}, Edit: true})
	if err != nil {
		t.Fatal(err)
	}
	return srv, filepath.Join(dir, "workflow.yaml")
}

func TestTheAnswerOfAnEndpointCanBeChanged(t *testing.T) {
	srv, file := editEndpoint(t)
	save := func(edits ...any) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"workflow": "moderate-comment", "rev": currentState(t, srv).Workflows[0].Rev, "edits": edits})
		return post(srv, "/api/edit", "", string(body))
	}

	w := currentState(t, srv).Workflows[0]
	var labels []string
	for _, f := range w.Respond.Edit {
		labels = append(labels, f.Group+"/"+f.Label+" ("+f.Kind+") = "+f.Value)
	}
	want := []string{"/Status (number) = ", "Body/allowed (text) = ${check.allowed.value}", "Body/reason (text) = ${check.reason.value}"}
	if strings.Join(labels, "\n") != strings.Join(want, "\n") || w.Respond.Edit[0].Hint != "Empty means 200." {
		t.Fatalf("fields of the answer:\n%s", strings.Join(labels, "\n"))
	}
	if schema := w.Edit[1]; schema.Label != "Body schema" || schema.Value != "schemas/comment.json" {
		t.Fatalf("body schema field = %+v", schema)
	}

	// The status is written as a number, behind nothing else that changes.
	before := readFile(t, file)
	if rec := save(set("201", "respond", "status"), set("${check.reason.value != \"none\"}", "respond", "body", "reason")); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	after := strings.NewReplacer(
		"    reason: ${check.reason.value}\n", "    reason: ${check.reason.value != \"none\"}\n  status: 201\n",
	).Replace(before)
	if got := readFile(t, file); got != after {
		t.Errorf("file after the change:\n%s\nwant:\n%s", got, after)
	}
	rec := post(srv, "/moderate", "", `{"text": "hello"}`)
	if rec.Code != http.StatusCreated || strings.TrimSpace(rec.Body.String()) != `{"allowed":true,"reason":false}` {
		t.Errorf("the endpoint now answers %d %s", rec.Code, rec.Body)
	}

	for value, want := range map[string]string{"abc": "Status must be a whole number", "99": "status must be an HTTP status from 200 to 599"} {
		if rec := save(set(value, "respond", "status")); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("status %q: %d %s; want %q", value, rec.Code, rec.Body, want)
		}
	}
	// Saved empty, the status and the schema go, and with them their effect.
	if rec := save(set("", "respond", "status"), set("", "trigger", "input")); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if got := readFile(t, file); strings.Contains(got, "  status: 201") || strings.Contains(got, "input: schemas") {
		t.Errorf("file after removing them:\n%s", got)
	}
	// A body without text is no longer refused at the door. It now gets as
	// far as the step that needs the text, and fails there.
	if rec := post(srv, "/moderate", "", `{"anything": true}`); rec.Code != http.StatusInternalServerError {
		t.Errorf("without a schema: %d %s", rec.Code, rec.Body)
	}
	if rec := post(srv, "/moderate", "", `{"text": "hello"}`); rec.Code != http.StatusOK {
		t.Errorf("without a status: %d %s", rec.Code, rec.Body)
	}

	// A schema is named again, and must exist.
	if rec := save(set("schemas/missing.json", "trigger", "input")); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "schema file not found") {
		t.Errorf("a schema that is missing: %d %s", rec.Code, rec.Body)
	}
	if rec := save(set("schemas/comment.json", "trigger", "input")); rec.Code != http.StatusOK || !strings.Contains(readFile(t, file), "  webhook: /moderate\n  input: schemas/comment.json\n") {
		t.Errorf("naming the schema again: %d %s\n%s", rec.Code, rec.Body, readFile(t, file))
	}

	// A server that does not allow editing says nothing of these fields.
	plain := currentState(t, serveEndpoint(t, moderator{})).Workflows[0]
	if plain.Respond == nil || plain.Respond.Edit != nil || plain.Edit != nil {
		t.Errorf("a read-only server offers editing: %+v", plain.Respond)
	}
}

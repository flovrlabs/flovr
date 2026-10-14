package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/simonyosb/flowmaker/internal/workflow"
)

func addStep(t *testing.T, srv *Server, add map[string]any) (int, string) {
	t.Helper()
	rev := ""
	for _, w := range currentState(t, srv).Workflows {
		if w.Name == "triage-ticket" {
			rev = w.Rev
		}
	}
	body, _ := json.Marshal(map[string]any{"workflow": "triage-ticket", "rev": rev, "add": add})
	rec := post(srv, "/api/steps", "", string(body))
	return rec.Code, rec.Body.String()
}

func TestStepsThatDoTheirOwnWorkAreAdded(t *testing.T) {
	srv, dir := editExample(t)

	code, body := addStep(t, srv, map[string]any{
		"id": "reply_draft", "after": "summarize", "kind": "ai", "when": `classify.category.value == "refund"`,
		"model": "openrouter/deepseek/deepseek-v4.1-flash", "input": "${trigger.body}",
		"prompt": "Write a short, kind reply.\n",
		"fields": []map[string]any{{"name": "reply", "type": "text", "required": true}},
	})
	if code != http.StatusOK {
		t.Fatalf("ai: status = %d, body %s", code, body)
	}
	if got := readFile(t, filepath.Join(dir, "prompts", "reply-draft.md")); got != "Write a short, kind reply.\n" {
		t.Errorf("prompt file = %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "schemas", "reply-draft.json")); !strings.Contains(got, `"reply": {`) {
		t.Errorf("schema file = %s", got)
	}

	code, body = addStep(t, srv, map[string]any{
		"id": "tone", "after": "classify", "kind": "decide", "model": "openrouter/cloudflare/clef", "input": "${trigger.body.message}",
		"questions": []map[string]any{
			{"name": "angry", "ask": "Is the customer angry?", "type": "yes or no", "yesIf": "They shout or threaten.", "noIf": "They are calm, even if unhappy."},
			{"name": "language", "ask": "Which language is it in?", "type": "one of", "answers": []map[string]string{{"name": "en", "meaning": "English"}, {"name": "th", "meaning": "Thai"}}},
			{"name": "risk", "ask": "How likely are they to leave?", "type": "score", "answers": []map[string]string{{"name": "low", "meaning": "Happy overall."}, {"name": "high", "meaning": "Says they will cancel."}}},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("decide: status = %d, body %s", code, body)
	}

	code, body = addStep(t, srv, map[string]any{"id": "log", "kind": "http", "method": "post", "url": "${env.BILLING_URL}"})
	if code != http.StatusOK {
		t.Fatalf("http: status = %d, body %s", code, body)
	}

	w, err := workflow.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range w.Steps {
		ids = append(ids, s.ID+":"+s.Kind())
	}
	if got := strings.Join(ids, " "); got != "classify:decide tone:decide summarize:ai reply_draft:ai alert_billing:http page_oncall:http log:http" {
		t.Errorf("steps = %s", got)
	}
	file := readFile(t, filepath.Join(dir, "workflow.yaml"))
	for _, want := range []string{
		"          score:\n            - low: Happy overall.\n            - high: Says they will cancel.\n",
		"        language:\n          ask: Which language is it in?\n          choice:\n            en: English\n            th: Thai\n",
		"      prompt: prompts/reply-draft.md\n",
		"    http:\n      method: POST\n      url: ${env.BILLING_URL}\n",
	} {
		if !strings.Contains(file, want) {
			t.Errorf("the file lacks %q:\n%s", want, file)
		}
	}
}

func TestStepsThatDoTheirOwnWorkAreRefusedWithoutLeavingFiles(t *testing.T) {
	srv, dir := editExample(t)
	before := readFile(t, filepath.Join(dir, "workflow.yaml"))
	for _, tc := range []struct {
		add  map[string]any
		want string
	}{
		{map[string]any{"id": "x", "kind": "ai", "model": "openrouter/a/b", "input": "${trigger.body}", "fields": []map[string]any{{"name": "a", "type": "text"}}}, "instructions"},
		{map[string]any{"id": "x", "kind": "ai", "model": "openrouter/a/b", "input": "${trigger.body}", "prompt": "Hi"}, "at least one field"},
		// The model is not one a provider is named for, which only the
		// whole workflow's check finds: the files written are taken away.
		{map[string]any{"id": "x", "kind": "ai", "model": "nonsense", "input": "${trigger.body}", "prompt": "Hi", "fields": []map[string]any{{"name": "a", "type": "text"}}}, "ai.model"},
		{map[string]any{"id": "x", "kind": "decide", "model": "openrouter/cloudflare/clef", "input": "${trigger.body}"}, "at least one question"},
		{map[string]any{"id": "x", "kind": "decide", "model": "openrouter/cloudflare/clef", "input": "${trigger.body}", "questions": []map[string]any{{"name": "q", "ask": "?", "type": "maybe"}}}, "yes or no, one of a list"},
		{map[string]any{"id": "x", "kind": "decide", "model": "openrouter/cloudflare/clef", "input": "${trigger.body}", "questions": []map[string]any{{"name": "q", "ask": "Which?", "type": "one of", "answers": []map[string]string{{"name": "a", "meaning": "A"}}}}}, "two answers"},
		{map[string]any{"id": "x", "kind": "http", "method": "FETCH", "url": "http://x"}, "method"},
		{map[string]any{"id": "x", "kind": "magic"}, "ai, decide or http"},
	} {
		code, body := addStep(t, srv, tc.add)
		if code == http.StatusOK || !strings.Contains(body, tc.want) {
			t.Errorf("%v: status = %d, body %s", tc.add, code, body)
		}
		if strings.Contains(body, `"line":`) && !strings.Contains(body, `"line":0`) {
			t.Errorf("%v: a problem names a line of a file that was never written: %s", tc.add, body)
		}
	}
	if got := readFile(t, filepath.Join(dir, "workflow.yaml")); got != before {
		t.Errorf("the workflow file changed")
	}
	for _, sub := range []string{"prompts", "schemas"} {
		entries, _ := os.ReadDir(filepath.Join(dir, sub))
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "x") {
				t.Errorf("%s/%s was left behind", sub, e.Name())
			}
		}
	}
}

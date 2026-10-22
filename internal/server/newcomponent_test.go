package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flovrlabs/flovr/internal/workflow"
)

func TestServiceIsMadeFromThePage(t *testing.T) {
	srv, root, _ := editFolder(t)
	rec := post(srv, "/api/components", "", `{"name": "ping", "description": "Tells a channel something happened.",
		"inputs": [{"name": "url", "type": "string"}, {"name": "text", "type": "string"}],
		"work": {"kind": "http", "method": "post", "url": "${input.url}"}}`)
	var answer struct{ Component workflowView }
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	dir := filepath.Join(root, "components", "ping")
	if made := answer.Component; made.Name != filepath.ToSlash(dir) || made.Title != "ping" || !made.Component || made.Rev == "" {
		t.Errorf("answer = %+v", made)
	}
	want := `flovr: 1
name: ping
description: Tells a channel something happened.

input:
  url: string
  text: string

http:
  method: POST
  url: ${input.url}
`
	if got := readFile(t, filepath.Join(dir, "component.yaml")); got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
	if _, err := workflow.LoadComponent(dir); err != nil {
		t.Errorf("the file does not load: %v", err)
	}

	// The page lists it at once, and a workflow can take a step from it.
	listed, offered := false, false
	for _, w := range currentState(t, srv).Workflows {
		listed = listed || w.Name == filepath.ToSlash(dir)
		if w.Name == "triage-ticket" {
			for _, c := range w.Components {
				offered = offered || c.Name == "ping"
			}
		}
	}
	if !listed || !offered {
		t.Errorf("listed %v, offered to a workflow %v", listed, offered)
	}
}

func TestAIServiceGetsItsPromptAndSchema(t *testing.T) {
	srv, root, _ := editFolder(t)
	rec := post(srv, "/api/components", "", `{"name": "summary",
		"inputs": [{"name": "ticket", "type": "object"}],
		"work": {"kind": "ai", "model": "openrouter/deepseek/deepseek-v4.1-flash", "input": "${input.ticket}",
			"prompt": "Sum up the ticket in a sentence.", "fields": [{"name": "text", "type": "text", "required": true}]}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	dir := filepath.Join(root, "components", "summary")
	if got := readFile(t, filepath.Join(dir, "prompts", "summary.md")); got != "Sum up the ticket in a sentence.\n" {
		t.Errorf("prompt = %q", got)
	}
	c, err := workflow.LoadComponent(dir)
	if err != nil {
		t.Fatalf("the file does not load: %v", err)
	}
	if c.Kind() != "ai" || c.AI.Prompt != "prompts/summary.md" || c.AI.Output != "schemas/summary.json" || strings.Join(c.Gives(), " ") != "text" {
		t.Errorf("component = %+v, gives %v", c.AI, c.Gives())
	}
}

func TestServiceIsRefused(t *testing.T) {
	srv, root, _ := editFolder(t)
	for name, body := range map[string]string{
		"no name":         `{"name": "", "work": {"kind": "http", "method": "GET", "url": "https://example.org"}}`,
		"a name with /":   `{"name": "a/b", "work": {"kind": "http", "method": "GET", "url": "https://example.org"}}`,
		"an unknown kind": `{"name": "x", "work": {"kind": "shell"}}`,
		"a bad input":     `{"name": "x", "inputs": [{"name": "1st", "type": "string"}], "work": {"kind": "http", "method": "GET", "url": "https://example.org"}}`,
		"a bad type":      `{"name": "x", "inputs": [{"name": "a", "type": "date"}], "work": {"kind": "http", "method": "GET", "url": "https://example.org"}}`,
		"two of a name":   `{"name": "x", "inputs": [{"name": "a", "type": "string"}, {"name": "a", "type": "number"}], "work": {"kind": "http", "method": "GET", "url": "https://example.org"}}`,
		"no questions":    `{"name": "x", "work": {"kind": "decide", "model": "openrouter/cloudflare/clef", "input": "${input}"}}`,
		"no prompt":       `{"name": "x", "work": {"kind": "ai", "model": "m", "input": "${input}", "fields": [{"name": "a", "type": "text"}]}}`,
		"an unknown read": `{"name": "x", "work": {"kind": "http", "method": "GET", "url": "${input.nothing}"}}`,
	} {
		if rec := post(srv, "/api/components", "", body); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, body %s", name, rec.Code, rec.Body)
		}
	}
	// Nothing is left behind, not even the folder made to hold it.
	if _, err := os.Stat(filepath.Join(root, "components")); !os.IsNotExist(err) {
		t.Errorf("the components folder is left: %v", err)
	}

	// Nor is anything written over.
	if rec := post(srv, "/api/components", "", `{"name": "ping", "work": {"kind": "http", "method": "GET", "url": "https://example.org"}}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	before := readFile(t, filepath.Join(root, "components", "ping", "component.yaml"))
	if rec := post(srv, "/api/components", "", `{"name": "ping", "work": {"kind": "http", "method": "POST", "url": "https://example.org/other"}}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a second ping: status = %d", rec.Code)
	}
	if readFile(t, filepath.Join(root, "components", "ping", "component.yaml")) != before {
		t.Error("the service was written over")
	}
}

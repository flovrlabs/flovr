package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3/option"
)

// fakeChatAPI serves one canned response for POST /v1/chat/completions and
// records the request it received.
func fakeChatAPI(t *testing.T, status int, response string) (baseURL string, body *map[string]any, header *http.Header) {
	t.Helper()
	var gotBody map[string]any
	var gotHeader http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		gotHeader = r.Header
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1", &gotBody, &gotHeader
}

func completion(finishReason, content, refusal string) string {
	b, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-1", "object": "chat.completion", "created": 0, "model": "gpt-test-2026",
		"choices": []any{map[string]any{
			"index": 0, "finish_reason": finishReason,
			"message": map[string]any{"role": "assistant", "content": content, "refusal": refusal},
		}},
		"usage": map[string]any{"prompt_tokens": 120, "completion_tokens": 30, "total_tokens": 150},
	})
	return string(b)
}

func TestOpenAIGenerate(t *testing.T) {
	baseURL, body, header := fakeChatAPI(t, http.StatusOK, completion("stop", `{"category":"refund"}`, ""))
	t.Setenv("OPENAI_API_KEY", "openai-key")
	client := newOpenAI("OpenAI", "OPENAI_API_KEY", baseURL)

	req := request
	req.Model = "gpt-test"
	resp, err := client.Generate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	want := Response{Text: `{"category":"refund"}`, Model: "gpt-test-2026", InputTokens: 120, OutputTokens: 30}
	if resp != want {
		t.Errorf("response = %+v, want %+v", resp, want)
	}

	sent, _ := json.Marshal(*body)
	wantSent := `{"messages":[{"content":"Classify the ticket.","role":"system"},{"content":"I was charged twice.","role":"user"}],` +
		`"model":"gpt-test",` +
		`"response_format":{"json_schema":{"name":"output","schema":{"properties":{"category":{"type":"string"}},"type":"object"},"strict":true},"type":"json_schema"}}`
	if string(sent) != wantSent {
		t.Errorf("request body:\n got %s\nwant %s", sent, wantSent)
	}
	if got := header.Get("Authorization"); got != "Bearer openai-key" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestOpenAISettingsDoNotReachAnotherService(t *testing.T) {
	baseURL, _, header := fakeChatAPI(t, http.StatusOK, completion("stop", `{}`, ""))
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("OPENAI_ORG_ID", "org-123")
	t.Setenv("OPENAI_PROJECT_ID", "proj-123")
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:1/unused")
	t.Setenv("OPENROUTER_API_KEY", "openrouter-key")

	client := newOpenAI("OpenRouter", "OPENROUTER_API_KEY", baseURL)
	if _, err := client.Generate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if got := header.Get("Authorization"); got != "Bearer openrouter-key" {
		t.Errorf("Authorization = %q, want the OpenRouter key", got)
	}
	for _, name := range []string{"OpenAI-Organization", "OpenAI-Project"} {
		if got := header.Get(name); got != "" {
			t.Errorf("%s = %q was sent to another service", name, got)
		}
	}
}

func TestOpenAIMissingKeyMakesNoRequest(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a request was sent without a key")
	}))
	t.Cleanup(srv.Close)

	_, err := newOpenAI("OpenRouter", "OPENROUTER_API_KEY", srv.URL).Generate(context.Background(), request)
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) || err.Error() != "no OpenRouter credentials found; set OPENROUTER_API_KEY" {
		t.Errorf("error = %v", err)
	}
}

func TestOpenAIFailures(t *testing.T) {
	apiError := `{"error":{"message":"You exceeded your current quota.","type":"insufficient_quota"}}`
	tests := []struct {
		name        string
		status      int
		response    string
		want        string
		unavailable bool
	}{
		{"refusal", http.StatusOK, completion("stop", "", "I can't help with that."), "declined", false},
		{"content filter", http.StatusOK, completion("content_filter", "", ""), "declined", false},
		{"cut off", http.StatusOK, completion("length", `{"categ`, ""), "cut off", false},
		{"no choices", http.StatusOK, `{"id":"x","object":"chat.completion","created":0,"model":"m","choices":[]}`, "no answer", false},
		{"bad key", http.StatusUnauthorized, apiError, "OpenAI rejected the key in OPENAI_API_KEY: You exceeded your current quota.", true},
		{
			"expired key, in OpenRouter's error shape", http.StatusUnauthorized,
			`{"error":{"message":"API key expired.","code":401,"metadata":{"headers":{}}}}`,
			"OpenAI rejected the key in OPENAI_API_KEY: API key expired.", true,
		},
		{"bad key without a reason", http.StatusUnauthorized, `{}`, "OpenAI rejected the key in OPENAI_API_KEY", true},
		{"quota", http.StatusTooManyRequests, apiError, "OpenAI answered 429: You exceeded your current quota.", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseURL, _, _ := fakeChatAPI(t, tt.status, tt.response)
			t.Setenv("OPENAI_API_KEY", "openai-key")
			client := newOpenAI("OpenAI", "OPENAI_API_KEY", baseURL, option.WithMaxRetries(0))
			_, err := client.Generate(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want one containing %q", err, tt.want)
			}
			var unavailable *UnavailableError
			if got := errors.As(err, &unavailable); got != tt.unavailable {
				t.Errorf("error is an UnavailableError: %v, want %v", got, tt.unavailable)
			}
		})
	}
}

package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"
)

// fakeAPI serves one canned response for POST /v1/messages and records the
// request body it received.
func fakeAPI(t *testing.T, status int, response string) (*Anthropic, *map[string]any) {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)
	client := NewAnthropic(option.WithBaseURL(srv.URL), option.WithAPIKey("test"), option.WithMaxRetries(0))
	return client, &got
}

func message(stopReason, text string) string {
	b, _ := json.Marshal(map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-haiku-5-5",
		"content":     []any{map[string]any{"type": "text", "text": text}},
		"stop_reason": stopReason,
		"usage":       map[string]any{"input_tokens": 120, "output_tokens": 30},
	})
	return string(b)
}

var request = Request{
	Model:  "claude-haiku-5-5",
	System: "Classify the ticket.",
	Input:  "I was charged twice.",
	Schema: map[string]any{
		"$schema":    "https://json-schema.org/draft/2020-12/schema",
		"type":       "object",
		"properties": map[string]any{"category": map[string]any{"type": "string"}},
	},
}

func TestGenerate(t *testing.T) {
	client, got := fakeAPI(t, http.StatusOK, message("end_turn", `{"category":"refund"}`))
	resp, err := client.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	want := Response{Text: `{"category":"refund"}`, Model: "claude-haiku-5-5", InputTokens: 120, OutputTokens: 30}
	if resp != want {
		t.Errorf("response = %+v, want %+v", resp, want)
	}

	sent, _ := json.Marshal(*got)
	wantSent := `{"max_tokens":16000,` +
		`"messages":[{"content":[{"text":"I was charged twice.","type":"text"}],"role":"user"}],` +
		`"model":"claude-haiku-5-5",` +
		`"output_config":{"format":{"schema":{"properties":{"category":{"type":"string"}},"type":"object"},"type":"json_schema"}},` +
		`"system":[{"text":"Classify the ticket.","type":"text"}]}`
	if string(sent) != wantSent {
		t.Errorf("request body:\n got %s\nwant %s", sent, wantSent)
	}
	if _, ok := request.Schema["$schema"]; !ok {
		t.Error("Generate modified the caller's schema")
	}
}

func TestGenerateFailures(t *testing.T) {
	apiError := `{"type":"error","error":{"type":"x","message":"nope"}}`
	tests := []struct {
		name        string
		status      int
		response    string
		want        string
		unavailable bool
	}{
		{"refusal", http.StatusOK, message("refusal", ""), "declined", false},
		{"cut off", http.StatusOK, message("max_tokens", `{"categ`), "cut off", false},
		{"bad key", http.StatusUnauthorized, apiError, "ANTHROPIC_API_KEY", true},
		{"unknown model", http.StatusNotFound, apiError, `does not know the model "claude-haiku-5-5"`, true},
		{"rate limit", http.StatusTooManyRequests, apiError, "rate limit", true},
		{"other", http.StatusBadRequest, apiError, "400", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := fakeAPI(t, tt.status, tt.response)
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

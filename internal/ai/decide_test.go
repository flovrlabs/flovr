package ai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// fakeDecisionsAPI serves one canned response and records the request body
// and headers it received.
func fakeDecisionsAPI(t *testing.T, status int, response string) (client *OpenRouterDecisions, body *string, header *http.Header) {
	t.Helper()
	var gotBody string
	var gotHeader http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		gotBody, gotHeader = string(data), r.Header
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)
	return &OpenRouterDecisions{url: srv.URL, key: "router-key", client: srv.Client()}, &gotBody, &gotHeader
}

var decideRequest = DecideRequest{
	Model: "cloudflare/clef",
	State: map[string]any{"subject": "Charged twice"},
	Questions: map[string]Question{
		"category": {
			Ask: "What does the customer need?", Kind: Choice,
			Options: []Option{{"refund", "They want money back."}, {"billing", ""}},
		},
		"priority": {
			Ask: "How urgent?", Kind: Score,
			Options: []Option{{"low", "No time pressure."}, {"medium", ""}, {"high", "Cannot use the product."}},
		},
		"spam":  {Ask: "Is this spam?", Kind: YesNo},
		"angry": {Ask: "Is the customer angry?", Kind: YesNo, YesIf: "Hostile wording.", NoIf: "Neutral or polite wording."},
	},
}

const decideAnswers = `{
	"category": {"type": "choice", "choice": "refund", "probabilities": {"refund": 0.95, "billing": 0.05}, "confidence": 0.89},
	"priority": {"type": "score", "score": 0.7, "legend": {"0": "low: No time pressure."}, "probabilities": {"0": 0.39, "1": 0.52, "2": 0.09}, "confidence": 0.15},
	"spam": {"type": "noul", "noul": 0.02},
	"angry": {"type": "noul", "noul": 0.75}
}`

func decisions(answers string) string {
	return `{"model": "cloudflare/clef", "answers": ` + answers + `,
		"usage": {"input_tokens": 416, "output_tokens": 0, "cost": 0.0001}, "id": "gen-1", "provider": "Cloudflare"}`
}

func TestDecide(t *testing.T) {
	client, body, header := fakeDecisionsAPI(t, http.StatusOK, decisions(decideAnswers))
	resp, err := client.Decide(context.Background(), decideRequest)
	if err != nil {
		t.Fatal(err)
	}

	// Choice answers keep the order they were written in: refund before billing.
	wantBody := `{"model":"cloudflare/clef","questions":{` +
		`"angry":{"type":"noul","instructions":"Is the customer angry?","criteria":{"false":"Neutral or polite wording.","true":"Hostile wording."}},` +
		`"category":{"type":"choice","instructions":"What does the customer need?","criteria":{"refund":"They want money back.","billing":null}},` +
		`"priority":{"type":"score","instructions":"How urgent?","criteria":["low: No time pressure.","medium","high: Cannot use the product."]},` +
		`"spam":{"type":"noul","instructions":"Is this spam?"}},` +
		`"state":{"subject":"Charged twice"}}`
	if *body != wantBody {
		t.Errorf("request body:\n got %s\nwant %s", *body, wantBody)
	}
	if got := header.Get("Authorization"); got != "Bearer router-key" {
		t.Errorf("Authorization = %q", got)
	}

	want := DecideResponse{
		Model:       "cloudflare/clef",
		InputTokens: 416,
		Answers: map[string]any{
			"category": map[string]any{
				"value": "refund", "confidence": 0.89,
				"probabilities": map[string]any{"refund": 0.95, "billing": 0.05},
			},
			"priority": map[string]any{
				"value": "medium", "score": 0.7, "confidence": 0.15,
				"probabilities": map[string]any{"low": 0.39, "medium": 0.52, "high": 0.09},
			},
			"spam":  map[string]any{"value": false, "probability": 0.02},
			"angry": map[string]any{"value": true, "probability": 0.75},
		},
	}
	if !reflect.DeepEqual(resp, want) {
		t.Errorf("response:\n got %#v\nwant %#v", resp, want)
	}
}

func TestDecideScoreWithoutProbabilities(t *testing.T) {
	answers := strings.Replace(decideAnswers,
		`"score": 0.7, "legend": {"0": "low: No time pressure."}, "probabilities": {"0": 0.39, "1": 0.52, "2": 0.09}`,
		`"score": 1.6`, 1)
	client, _, _ := fakeDecisionsAPI(t, http.StatusOK, decisions(answers))
	resp, err := client.Decide(context.Background(), decideRequest)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"value": "high", "score": 1.6, "confidence": 0.15}
	if got := resp.Answers["priority"]; !reflect.DeepEqual(got, want) {
		t.Errorf("priority = %#v, want %#v", got, want)
	}
}

func TestDecideFailures(t *testing.T) {
	replace := func(old, new string) string {
		if !strings.Contains(decideAnswers, old) {
			t.Fatalf("decideAnswers has no %s", old)
		}
		return decisions(strings.Replace(decideAnswers, old, new, 1))
	}
	tests := []struct {
		name        string
		status      int
		response    string
		want        string
		unavailable bool
	}{
		{
			"question left unanswered", http.StatusOK,
			replace(`"spam": {"type": "noul", "noul": 0.02},`, ``),
			`the model did not answer the question "spam"`, false,
		},
		{
			"answer of the wrong type", http.StatusOK,
			replace(`"spam": {"type": "noul", "noul": 0.02}`, `"spam": {"type": "choice", "choice": "yes"}`),
			`the model's answer to "spam": is of type "choice", not "noul"`, false,
		},
		{
			"choice that was not offered", http.StatusOK,
			replace(`"choice": "refund"`, `"choice": "shipping"`),
			`the model's answer to "category": "shipping" is not one of the options`, false,
		},
		{
			"score missing a level", http.StatusOK,
			replace(`"1": 0.52, "2": 0.09`, `"1": 0.61`),
			`the model's answer to "priority": has no probability for level "high"`, false,
		},
		{"not JSON", http.StatusOK, `<html>`, "OpenRouter's response is not the JSON expected", false},
		{
			"expired key", http.StatusUnauthorized, `{"error":{"message":"API key expired.","code":401}}`,
			"OpenRouter rejected the key in OPENROUTER_API_KEY: API key expired.", true,
		},
		{
			"model that cannot decide", http.StatusBadRequest, `{"error":{"message":"Model does not support decisions."}}`,
			"OpenRouter answered 400: Model does not support decisions.", true,
		},
		{"error without a reason", http.StatusBadGateway, `<html>`, "OpenRouter answered 502: Bad Gateway", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _, _ := fakeDecisionsAPI(t, tt.status, tt.response)
			_, err := client.Decide(context.Background(), decideRequest)
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

func TestDecideMissingKeyMakesNoRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a request was sent without a key")
	}))
	t.Cleanup(srv.Close)

	client := &OpenRouterDecisions{url: srv.URL, client: srv.Client()}
	_, err := client.Decide(context.Background(), decideRequest)
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) || err.Error() != "no OpenRouter credentials found; set OPENROUTER_API_KEY" {
		t.Errorf("error = %v", err)
	}
}

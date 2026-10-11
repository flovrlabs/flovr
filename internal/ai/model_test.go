package ai

import (
	"context"
	"strings"
	"testing"
)

func TestSplitModel(t *testing.T) {
	tests := []struct {
		name, provider, model string
	}{
		{"anthropic/claude-haiku-5-5", "anthropic", "claude-haiku-5-5"},
		{"openai/gpt-test", "openai", "gpt-test"},
		{"openrouter/anthropic/claude-haiku", "openrouter", "anthropic/claude-haiku"},
	}
	for _, tt := range tests {
		provider, model, err := SplitModel(tt.name)
		if err != nil || provider != tt.provider || model != tt.model {
			t.Errorf("SplitModel(%q) = %q, %q, %v", tt.name, provider, model, err)
		}
	}
	for _, name := range []string{"claude-haiku-5-5", "openai/", "google/gemini", ""} {
		if _, _, err := SplitModel(name); err == nil {
			t.Errorf("SplitModel(%q) succeeded", name)
		}
	}
}

type recorder struct{ got Request }

func (r *recorder) Generate(_ context.Context, req Request) (Response, error) {
	r.got = req
	return Response{Text: "{}", Model: req.Model + "-2026"}, nil
}

func TestRouter(t *testing.T) {
	openrouter, openai := &recorder{}, &recorder{}
	router := &Router{providers: map[string]generator{"openrouter": openrouter, "openai": openai}}

	resp, err := router.Generate(context.Background(), Request{Model: "openrouter/anthropic/claude-haiku", Input: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if openrouter.got.Model != "anthropic/claude-haiku" || openrouter.got.Input != "hi" {
		t.Errorf("provider got %+v", openrouter.got)
	}
	if openai.got.Model != "" {
		t.Errorf("the wrong provider was also called with %+v", openai.got)
	}
	if want := "openrouter/anthropic/claude-haiku-2026"; resp.Model != want {
		t.Errorf("response model = %q, want %q", resp.Model, want)
	}

	_, err = router.Generate(context.Background(), Request{Model: "claude-haiku"})
	if err == nil || !strings.HasPrefix(err.Error(), `model "claude-haiku" must be written as provider/model`) {
		t.Errorf("error = %v", err)
	}
}

func (r *recorder) Decide(_ context.Context, req DecideRequest) (DecideResponse, error) {
	r.got = Request{Model: req.Model}
	return DecideResponse{Answers: map[string]any{}, Model: req.Model}, nil
}

func TestRouterDecide(t *testing.T) {
	openrouter := &recorder{}
	router := &Router{deciders: map[string]decider{"openrouter": openrouter}}

	resp, err := router.Decide(context.Background(), DecideRequest{Model: "openrouter/cloudflare/clef"})
	if err != nil {
		t.Fatal(err)
	}
	if openrouter.got.Model != "cloudflare/clef" || resp.Model != "openrouter/cloudflare/clef" {
		t.Errorf("provider got model %q, response model = %q", openrouter.got.Model, resp.Model)
	}

	_, err = router.Decide(context.Background(), DecideRequest{Model: "anthropic/claude-haiku-5-5"})
	want := `model "anthropic/claude-haiku-5-5" cannot decide; decision models come from openrouter`
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %s", err, want)
	}
}

func TestNewRouterKnowsEveryProvider(t *testing.T) {
	router := NewRouter()
	if len(router.providers) != len(Providers) || len(router.deciders) != len(DecideProviders) {
		t.Errorf("router has %d providers and %d deciders; the lists have %d and %d",
			len(router.providers), len(router.deciders), len(Providers), len(DecideProviders))
	}
	for _, name := range Providers {
		if router.providers[name] == nil {
			t.Errorf("no client for provider %q", name)
		}
	}
	for _, name := range DecideProviders {
		if router.deciders[name] == nil {
			t.Errorf("no decision client for provider %q", name)
		}
	}
}

package engine

import (
	"context"
	"testing"

	"github.com/flovrlabs/flovr/internal/ai"
)

func TestTokensAreCountedOverEveryTry(t *testing.T) {
	model := &flaky{errs: []error{nil}, answer: `{"summary": "down"}`}
	counted := &tokenModel{next: model, in: 100, out: 20}
	var res StepResult
	eng := &Engine{Model: counted, OnStep: func(r StepResult) { res = r }}
	_, err := eng.Run(context.Background(), summarizeFlow(t, "    retry: {times: 1, wait: 1ms, on: [invalid-answer]}\n"), map[string]any{"body": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if want := (Tokens{In: 200, Out: 40}); res.Tokens != want {
		t.Errorf("tokens = %+v, want %+v for two tries", res.Tokens, want)
	}
	if got := (Tokens{In: 1234567, Out: 89}).String(); got != "1,234,567 tokens in, 89 out" {
		t.Errorf("String() = %q", got)
	}
}

// tokenModel says every answer took the tokens it is given.
type tokenModel struct {
	next    Model
	in, out int64
}

func (m *tokenModel) Generate(ctx context.Context, req ai.Request) (ai.Response, error) {
	resp, err := m.next.Generate(ctx, req)
	resp.InputTokens, resp.OutputTokens = m.in, m.out
	return resp, err
}

func (m *tokenModel) Decide(ctx context.Context, req ai.DecideRequest) (ai.DecideResponse, error) {
	resp, err := m.next.Decide(ctx, req)
	resp.InputTokens = m.in
	return resp, err
}

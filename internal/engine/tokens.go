package engine

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/flovrlabs/flovr/internal/ai"
)

// Tokens is how many tokens models read and wrote.
type Tokens struct {
	In  int64 `json:"in"`
	Out int64 `json:"out"`
}

func (t *Tokens) Add(u Tokens) {
	t.In += u.In
	t.Out += u.Out
}

func (t Tokens) String() string {
	return fmt.Sprintf("%s tokens in, %s out", thousands(t.In), thousands(t.Out))
}

// thousands writes n with commas between groups of three digits.
func thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0 && s[i-1] != '-'; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// counting passes requests on to a model and adds up the tokens of the
// answers, across every try of a step and every step inside it.
type counting struct {
	next   Model
	mu     sync.Mutex
	tokens Tokens
}

func (c *counting) add(t Tokens) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokens.Add(t)
}

func (c *counting) total() Tokens {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokens
}

func (c *counting) Generate(ctx context.Context, req ai.Request) (ai.Response, error) {
	resp, err := c.next.Generate(ctx, req)
	c.add(Tokens{resp.InputTokens, resp.OutputTokens})
	return resp, err
}

func (c *counting) Decide(ctx context.Context, req ai.DecideRequest) (ai.DecideResponse, error) {
	resp, err := c.next.Decide(ctx, req)
	c.add(Tokens{In: resp.InputTokens})
	return resp, err
}

// Package ai calls the language model behind a workflow's ai steps.
package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Request is one model call whose answer must be JSON matching Schema.
type Request struct {
	Model  string
	System string
	Input  string
	Schema map[string]any
}

type Response struct {
	Text         string // the JSON document
	Model        string // the model that served the request
	InputTokens  int64
	OutputTokens int64
}

type Anthropic struct{ client anthropic.Client }

// NewAnthropic returns a client that takes its credentials from the
// environment, as the Anthropic SDK does by default.
func NewAnthropic(opts ...option.RequestOption) *Anthropic {
	return &Anthropic{client: anthropic.NewClient(opts...)}
}

func (a *Anthropic) Generate(ctx context.Context, req Request) (Response, error) {
	msg, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: 16000,
		System:    []anthropic.TextBlockParam{{Text: req.System}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(req.Input)),
		},
		OutputConfig: anthropic.OutputConfigParam{
			Format: anthropic.JSONOutputFormatParam{Schema: constraints(req.Schema)},
		},
	})
	if err != nil {
		return Response{}, describe(err, req.Model)
	}

	switch msg.StopReason {
	case anthropic.StopReasonRefusal:
		return Response{}, fmt.Errorf("the model declined this request (category %q)", msg.StopDetails.Category)
	case anthropic.StopReasonMaxTokens:
		return Response{}, errors.New("the model's answer was cut off before it finished")
	}

	var text strings.Builder
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	return Response{
		Text:         text.String(),
		Model:        string(msg.Model),
		InputTokens:  msg.Usage.InputTokens,
		OutputTokens: msg.Usage.OutputTokens,
	}, nil
}

func describe(err error, model string) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusUnauthorized:
			err = errors.New("Anthropic rejected the credentials; set ANTHROPIC_API_KEY to a valid key")
		case http.StatusNotFound:
			err = fmt.Errorf("Anthropic does not know the model %q", model)
		case http.StatusTooManyRequests:
			err = errors.New("Anthropic rate limit reached; try again shortly")
		}
	}
	return &UnavailableError{err}
}

package ai

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// OpenAI calls the Chat Completions API of OpenAI, or of another service that
// implements it, such as OpenRouter.
type OpenAI struct {
	chat   openai.ChatCompletionService
	name   string // the service, for error messages
	keyEnv string // the environment variable its key comes from
	hasKey bool
}

// NewOpenAI returns a client for OpenAI, with its key taken from
// OPENAI_API_KEY. OPENAI_BASE_URL points it at another server that speaks the
// same API.
func NewOpenAI(opts ...option.RequestOption) *OpenAI {
	baseURL := cmp.Or(os.Getenv("OPENAI_BASE_URL"), "https://api.openai.com/v1/")
	return newOpenAI("OpenAI", "OPENAI_API_KEY", baseURL, opts...)
}

// NewOpenRouter returns a client for OpenRouter, with its key taken from
// OPENROUTER_API_KEY.
func NewOpenRouter(opts ...option.RequestOption) *OpenAI {
	return newOpenAI("OpenRouter", "OPENROUTER_API_KEY", "https://openrouter.ai/api/v1/", opts...)
}

// newOpenAI builds the client from explicit settings only. The SDK's usual
// constructor also applies OPENAI_* environment variables, and those must not
// be sent to a different service.
func newOpenAI(name, keyEnv, baseURL string, opts ...option.RequestOption) *OpenAI {
	key := os.Getenv(keyEnv)
	opts = append([]option.RequestOption{option.WithBaseURL(baseURL), option.WithAPIKey(key)}, opts...)
	return &OpenAI{
		chat:   openai.NewChatCompletionService(opts...),
		name:   name,
		keyEnv: keyEnv,
		hasKey: key != "",
	}
}

func (o *OpenAI) Generate(ctx context.Context, req Request) (Response, error) {
	if !o.hasKey {
		return Response{}, &UnavailableError{fmt.Errorf("no %s credentials found; set %s", o.name, o.keyEnv)}
	}

	resp, err := o.chat.New(ctx, openai.ChatCompletionNewParams{
		Model: req.Model,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(req.System),
			openai.UserMessage(req.Input),
		},
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &openai.ResponseFormatJSONSchemaParam{
				JSONSchema: openai.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:   "output",
					Strict: openai.Bool(true),
					Schema: constraints(req.Schema),
				},
			},
		},
	})
	if err != nil {
		return Response{}, o.describe(err)
	}
	if len(resp.Choices) == 0 {
		return Response{}, errors.New("the model gave no answer")
	}

	choice := resp.Choices[0]
	switch {
	case choice.Message.Refusal != "" || choice.FinishReason == "content_filter":
		return Response{}, errors.New("the model declined this request")
	case choice.FinishReason == "length":
		return Response{}, errors.New("the model's answer was cut off before it finished")
	}
	return Response{
		Text:         choice.Message.Content,
		Model:        resp.Model,
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
	}, nil
}

func (o *OpenAI) describe(err error) error {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		err = statusError(o.name, o.keyEnv, apiErr.StatusCode, apiErr.Message)
	}
	return &UnavailableError{err}
}

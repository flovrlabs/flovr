package ai

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// OpenAI calls the Chat Completions API of OpenAI, or of another service that
// implements it, such as OpenRouter.
type OpenAI struct {
	chat   openai.ChatCompletionService
	name   string // the service, for error messages
	keyEnv string // the environment variable its key comes from, or "" for a service without keys
	hasKey bool
	where  string // the address it is reached at, for error messages
	local  bool   // Ollama, which runs where the user runs it
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

// NewOllama returns a client for Ollama, through the API it serves in the
// shape of OpenAI's. It runs on this machine unless OLLAMA_HOST says where,
// and needs no key.
func NewOllama(opts ...option.RequestOption) *OpenAI {
	o := newOpenAI("Ollama", "", ollamaURL(os.Getenv("OLLAMA_HOST")), opts...)
	o.local = true
	return o
}

// ollamaURL is the address of Ollama's API from OLLAMA_HOST, read as Ollama
// reads it: a host, host:port or a URL, with port 11434 when only a host is
// given.
func ollamaURL(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "127.0.0.1"
	}
	if !strings.Contains(host, "://") {
		host = "http://" + host
		if u, err := url.Parse(host); err == nil && u.Port() == "" {
			u.Host = net.JoinHostPort(u.Hostname(), "11434")
			host = u.String()
		}
	}
	return strings.TrimSuffix(host, "/") + "/v1/"
}

// newOpenAI builds the client from explicit settings only. The SDK's usual
// constructor also applies OPENAI_* environment variables, and those must not
// be sent to a different service.
func newOpenAI(name, keyEnv, baseURL string, opts ...option.RequestOption) *OpenAI {
	key := ""
	if keyEnv != "" {
		key = os.Getenv(keyEnv)
	}
	opts = append([]option.RequestOption{option.WithBaseURL(baseURL), option.WithAPIKey(key)}, opts...)
	return &OpenAI{
		chat:   openai.NewChatCompletionService(opts...),
		name:   name,
		keyEnv: keyEnv,
		hasKey: keyEnv == "" || key != "",
		where:  strings.TrimSuffix(strings.TrimSuffix(baseURL, "/"), "/v1"),
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
		return Response{}, o.describe(err, req.Model)
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

func (o *OpenAI) describe(err error, model string) error {
	var apiErr *openai.Error
	var netErr *net.OpError
	switch {
	case errors.As(err, &apiErr):
		err = statusError(o.name, o.keyEnv, apiErr.StatusCode, apiErr.Message)
		if o.local && apiErr.StatusCode == http.StatusNotFound {
			err = fmt.Errorf("%w; fetch it with: ollama pull %s", err, model)
		}
	case o.local && errors.As(err, &netErr) && netErr.Op == "dial":
		err = &unreachableError{fmt.Sprintf("%s could not be reached at %s; is it running?", o.name, o.where), err}
	}
	return &UnavailableError{err}
}

// unreachableError says plainly that a service could not be reached, and
// keeps the network error, by which a step's retry knows to try again.
type unreachableError struct {
	text string
	err  error
}

func (e *unreachableError) Error() string { return e.text }
func (e *unreachableError) Unwrap() error { return e.err }

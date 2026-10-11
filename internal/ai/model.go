package ai

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
)

// Providers are what a model name can start with, as in "openai/gpt-5".
var Providers = []string{"anthropic", "openai", "openrouter"}

// SplitModel splits a model name written as provider/model. The model part
// may itself contain slashes, as OpenRouter's names do.
func SplitModel(name string) (provider, model string, err error) {
	provider, model, _ = strings.Cut(name, "/")
	if model == "" || !slices.Contains(Providers, provider) {
		return "", "", errors.New("must be written as provider/model, where provider is one of " + strings.Join(Providers, ", "))
	}
	return provider, model, nil
}

// DecideProviders are the providers that serve decision models, which answer
// typed questions instead of writing text.
var DecideProviders = []string{"openrouter"}

type generator interface {
	Generate(ctx context.Context, req Request) (Response, error)
}

type decider interface {
	Decide(ctx context.Context, req DecideRequest) (DecideResponse, error)
}

// Router sends each request to the provider its model name starts with.
type Router struct {
	providers map[string]generator
	deciders  map[string]decider
}

// NewRouter returns a Router for every provider in Providers and
// DecideProviders, each taking its credentials from the environment.
func NewRouter() *Router {
	return &Router{
		providers: map[string]generator{
			"anthropic":  NewAnthropic(),
			"openai":     NewOpenAI(),
			"openrouter": NewOpenRouter(),
		},
		deciders: map[string]decider{
			"openrouter": NewOpenRouterDecisions(),
		},
	}
}

func (r *Router) Decide(ctx context.Context, req DecideRequest) (DecideResponse, error) {
	provider, model, err := SplitModel(req.Model)
	if err != nil {
		return DecideResponse{}, fmt.Errorf("model %q %w", req.Model, err)
	}
	d, ok := r.deciders[provider]
	if !ok {
		return DecideResponse{}, fmt.Errorf("model %q cannot decide; decision models come from %s", req.Model, strings.Join(DecideProviders, ", "))
	}
	req.Model = model
	resp, err := d.Decide(ctx, req)
	if err != nil {
		return DecideResponse{}, err
	}
	resp.Model = provider + "/" + resp.Model
	return resp, nil
}

func (r *Router) Generate(ctx context.Context, req Request) (Response, error) {
	provider, model, err := SplitModel(req.Model)
	if err != nil {
		return Response{}, fmt.Errorf("model %q %w", req.Model, err)
	}
	req.Model = model
	resp, err := r.providers[provider].Generate(ctx, req)
	if err != nil {
		return Response{}, err
	}
	resp.Model = provider + "/" + resp.Model
	return resp, nil
}

// UnavailableError is a failure to get any answer from the model, such as bad
// credentials, an unknown model, or a network or rate-limit error. It says
// nothing about the request that was being made.
type UnavailableError struct{ Err error }

func (e *UnavailableError) Error() string { return e.Err.Error() }
func (e *UnavailableError) Unwrap() error { return e.Err }

// statusError describes an error status from a model service, with the reason
// the service gave if it gave one.
func statusError(service, keyEnv string, status int, reason string) error {
	if status == http.StatusUnauthorized {
		if reason == "" {
			return fmt.Errorf("%s rejected the key in %s", service, keyEnv)
		}
		return fmt.Errorf("%s rejected the key in %s: %s", service, keyEnv, reason)
	}
	if reason == "" {
		reason = http.StatusText(status)
	}
	return fmt.Errorf("%s answered %d: %s", service, status, reason)
}

// constraints returns schema without $schema. That key tells editors which
// dialect a schema file uses; it is not a constraint, and model APIs only
// accept constraints.
func constraints(schema map[string]any) map[string]any {
	schema = maps.Clone(schema)
	delete(schema, "$schema")
	return schema
}

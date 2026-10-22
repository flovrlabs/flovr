package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/flovrlabs/flovr/internal/ai"
	"github.com/flovrlabs/flovr/internal/expr"
	"github.com/flovrlabs/flovr/internal/workflow"
)

// InvalidAnswerError is an answer of a model that an ai step cannot use:
// empty, not JSON, or not what the schema asks for. Raw is the answer as the
// model wrote it.
type InvalidAnswerError struct {
	Raw string
	err error
}

func (e *InvalidAnswerError) Error() string { return e.err.Error() }
func (e *InvalidAnswerError) Unwrap() error { return e.err }

// HTTPStatusError is an error status that the server an http step sent its
// request to answered with.
type HTTPStatusError struct {
	Status int
	Text   string // such as "503 Service Unavailable"
}

func (e *HTTPStatusError) Error() string { return "the server answered " + e.Text }

// failureOf names the kind of failure err is, one of workflow.Failures, or ""
// for one that trying again would not change, such as a request the service
// refused or an expression that is wrong.
func failureOf(err error) string {
	var status int
	var model *ai.StatusError
	var server *HTTPStatusError
	switch {
	case errors.As(err, &model):
		status = model.Status
	case errors.As(err, &server):
		status = server.Status
	}
	switch {
	case status == http.StatusTooManyRequests:
		return workflow.RateLimit
	case status >= 500:
		return workflow.Unavailable
	case status != 0:
		return ""
	}
	var answer *InvalidAnswerError
	if errors.As(err, &answer) {
		return workflow.InvalidAnswer
	}
	// The service could not be reached, or the connection broke.
	var netErr net.Error
	if errors.As(err, &netErr) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) {
		return workflow.Unavailable
	}
	return ""
}

// do runs a step that is to run: once, or again after a failure its retry
// names, each try within the step's timeout.
func (e *Engine) do(ctx context.Context, f files, s workflow.Step, env *expr.Env, vars map[string]any, res *StepResult) {
	for n := 1; ; n++ {
		var failure string
		res.Output, res.Detail, res.Steps, failure, res.Err = e.try(ctx, f, s, env, vars)
		res.Tries = n
		if res.Err == nil {
			if n > 1 {
				res.Detail = fmt.Sprintf("%s · on try %d", res.Detail, n)
			}
			return
		}
		if ctx.Err() != nil || s.Retry == nil || n > s.Retry.Times || !s.Retry.After(failure) {
			if n > 1 {
				res.Err = fmt.Errorf("after %d tries: %w", n, res.Err)
			}
			return
		}
		select {
		case <-time.After(s.Retry.Pause(n + 1)):
		case <-ctx.Done():
			res.Err = fmt.Errorf("after %d tries: %w", n, ctx.Err())
			return
		}
	}
}

// try runs the step once, and says what kind of failure it met, if any.
func (e *Engine) try(ctx context.Context, f files, s workflow.Step, env *expr.Env, vars map[string]any) (out any, detail string, inner []StepResult, failure string, err error) {
	limited := ctx
	if s.Timeout != nil {
		var cancel context.CancelFunc
		limited, cancel = context.WithTimeout(ctx, time.Duration(*s.Timeout))
		defer cancel()
	}
	switch {
	case s.AI != nil:
		out, detail, err = e.ai(limited, f, s.AI, env, vars)
	case s.Decide != nil:
		out, detail, err = e.decide(limited, s.Decide, env, vars)
	case s.HTTP != nil:
		out, detail, err = e.http(limited, s.HTTP, env, vars)
	case s.Component() != nil:
		out, detail, inner, err = e.use(limited, s, env, vars)
	}
	if err != nil && s.Timeout != nil && ctx.Err() == nil && errors.Is(limited.Err(), context.DeadlineExceeded) {
		return out, detail, inner, workflow.TimedOut, fmt.Errorf("took longer than its timeout of %s", s.Timeout)
	}
	return out, detail, inner, failureOf(err), err
}

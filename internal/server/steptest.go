package server

import (
	"context"
	"net/http"
	"slices"

	"github.com/simonyosb/flowmaker/internal/engine"
	"github.com/simonyosb/flowmaker/internal/workflow"
)

// stepTestRequest tries one step of a workflow on its own.
type stepTestRequest struct {
	Workflow string `json:"workflow"`
	Step     string `json:"step"`
	// Run is the run whose request and earlier steps' outputs the step is
	// given. Without it the step gets Trigger and nothing from earlier steps.
	Run     string `json:"run"`
	Trigger any    `json:"trigger"`
}

// stepTestView is what a step did when it was tried on its own.
type stepTestView struct {
	Step Step   `json:"step"`
	From string `json:"from,omitempty"` // the run it was given the data of
	// WouldRun is false when the step's condition was false for this data:
	// in a run the step would have been skipped. It was tried all the same.
	WouldRun bool `json:"wouldRun"`
}

// stepTest runs one step of a workflow as it is being served, with the data
// of a run that has happened, so that a step can be tried without running
// the steps before it again. Models are called for real; http steps are not
// sent, as in tests. Nothing is recorded as a run.
func (s *Server) stepTest(req stepTestRequest) (int, any) {
	live := s.live.Load()
	i := slices.IndexFunc(live.workflows, func(w *workflow.Workflow) bool { return w.Name == req.Workflow })
	if i < 0 {
		return http.StatusNotFound, fail("no workflow is named %s", req.Workflow)
	}
	w := live.workflows[i]
	at := slices.IndexFunc(w.Steps, func(st workflow.Step) bool { return st.ID == req.Step })
	if at < 0 {
		return http.StatusNotFound, fail("%s has no step %s", w.Name, req.Step)
	}

	trigger := req.Trigger
	earlier := map[string]any{}
	if req.Run != "" {
		run, ok := s.history.get(req.Run)
		if !ok || run.Workflow != w.Name {
			return http.StatusNotFound, fail("%s has no run %s", w.Name, req.Run)
		}
		if run.Trigger == nil {
			return http.StatusUnprocessableEntity, fail("The run's record does not hold the event that started it.")
		}
		trigger = run.Trigger
		for _, done := range run.Steps {
			if done.Status == "ok" {
				earlier[done.ID] = done.Output
			}
		}
	}
	if trigger == nil {
		return http.StatusUnprocessableEntity, fail("Give the step a run to take its data from, or a request to start from.")
	}

	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	eng := &engine.Engine{Model: s.opts.Model, HTTP: s.opts.HTTP, Env: s.opts.Env, StubHTTP: true}
	res, wouldRun := eng.RunStep(ctx, w, at, trigger, earlier)
	s.opts.Log("%s: step %s tried on its own with the data of %s: %s", w.Name, req.Step, orNone(req.Run), res.Status)
	return http.StatusOK, stepTestView{Step: record(res), From: req.Run, WouldRun: wouldRun}
}

func orNone(run string) string {
	if run == "" {
		return "a given request"
	}
	return run
}

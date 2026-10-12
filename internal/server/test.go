package server

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"aia/internal/ai"
	"aia/internal/engine"
	"aia/internal/workflow"
)

// testRequest starts tests of a workflow.
type testRequest struct {
	Workflow string `json:"workflow"`
	// Test is the position of the test to run, counted from 0. All of the
	// workflow's tests run if it is absent.
	Test *int `json:"test"`
}

// started names a run that a request started.
type started struct {
	Run  string `json:"run"`
	Test string `json:"test"`
}

// test starts tests of the workflow as it is being served. Each test is a
// run of its own, which the page follows like any other. They run one after
// another, so that a workflow's tests do not all call their models at once.
func (s *Server) test(req testRequest) (int, any) {
	live := s.live.Load()
	i := slices.IndexFunc(live.workflows, func(w *workflow.Workflow) bool { return w.Name == req.Workflow })
	if i < 0 {
		if components, _ := s.components(); components[req.Workflow] != nil {
			return s.testComponent(components[req.Workflow], req)
		}
		return http.StatusNotFound, fail("no workflow is named %s", req.Workflow)
	}
	w := live.workflows[i]
	tests := w.Tests
	switch {
	case len(tests) == 0:
		return http.StatusUnprocessableEntity, fail("%s has no tests. They are written under tests: in %s.", w.Name, w.Path)
	case req.Test != nil && (*req.Test < 0 || *req.Test >= len(tests)):
		return http.StatusUnprocessableEntity, fail("%s has no test at position %d", w.Name, *req.Test)
	case req.Test != nil:
		tests = tests[*req.Test : *req.Test+1]
	}

	runs := make([]started, len(tests))
	for j, t := range tests {
		runs[j] = started{Run: newID(), Test: testName(t)}
	}
	s.running.Add(1)
	go func() {
		defer s.running.Done()
		for j, t := range tests {
			s.runTest(runs[j].Run, w, t)
		}
	}()
	return http.StatusAccepted, map[string]any{"runs": runs}
}

func testName(t workflow.Test) string {
	if t.Name != "" {
		return t.Name
	}
	return t.Fixture
}

// runTest runs one test to completion and records it as a run. The run
// failed if a step failed or if the test expected something else.
func (s *Server) runTest(id string, w *workflow.Workflow, t workflow.Test) Run {
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()

	run := Run{
		ID: id, Workflow: w.Name, Source: "test", Test: testName(t), Commit: s.commit(),
		Started: time.Now(), Status: running, Steps: []Step{},
	}
	if trigger, err := w.Fixture(t.Fixture); err == nil {
		run.Trigger = trigger
	}
	s.history.put(run)
	s.opts.Log("%s %s started by test %q", id, w.Name, run.Test)

	eng := &engine.Engine{
		Model: s.opts.Model,
		Env:   s.opts.Env,
		OnStep: func(r engine.StepResult) {
			run.Steps = append(run.Steps, record(r))
			s.history.put(run)
		},
	}
	res, err := eng.Test(ctx, w, t)
	run.Response = res.Response
	s.finishTest(&run, res, err)
	s.opts.Log("%s %s test %q %s in %s", id, w.Name, run.Test, run.Status, time.Duration(run.Millis)*time.Millisecond)
	return run
}

// finishTest records how a test's run ended.
func (s *Server) finishTest(run *Run, res engine.TestResult, err error) {
	run.Checks = res.Checks
	run.Status = "ok"
	var unavailable *ai.UnavailableError
	switch {
	case errors.As(err, &unavailable):
		run.Status, run.Error = "failed", "The test could not run, which says nothing about the workflow: "+err.Error()
	case err != nil:
		run.Status, run.Error = "failed", err.Error()
	case len(res.Checks) == 0 && len(res.Failures) > 0:
		// A step failed, so nothing the test expected was looked at.
		run.Status, run.Error = "failed", res.Failures[0]
	case len(res.Failures) > 0:
		run.Status = "failed"
	}
	run.Millis = time.Since(run.Started).Milliseconds()
	s.history.put(*run)
}

// testComponent starts tests of a component, as test does for a workflow.
func (s *Server) testComponent(c *workflow.Component, req testRequest) (int, any) {
	tests, names := c.Tests, componentCaseNames(c)
	switch {
	case len(tests) == 0:
		return http.StatusUnprocessableEntity, fail("%s has no tests. They are written under tests: in %s.", c.Name, c.Path)
	case req.Test != nil && (*req.Test < 0 || *req.Test >= len(tests)):
		return http.StatusUnprocessableEntity, fail("%s has no test at position %d", c.Name, *req.Test)
	case req.Test != nil:
		tests, names = tests[*req.Test:*req.Test+1], names[*req.Test:*req.Test+1]
	}
	runs := make([]started, len(tests))
	for j := range tests {
		runs[j] = started{Run: newID(), Test: names[j]}
	}
	s.running.Add(1)
	go func() {
		defer s.running.Done()
		for j, t := range tests {
			s.runComponentTest(runs[j].Run, c, t, names[j])
		}
	}()
	return http.StatusAccepted, map[string]any{"runs": runs}
}

package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/flovrlabs/flovr/internal/ai"
	"github.com/flovrlabs/flovr/internal/engine"
	"github.com/flovrlabs/flovr/internal/workflow"
)

// testRequest starts tests of a workflow.
type testRequest struct {
	Workflow string `json:"workflow"`
	// Test is the position of the test to run, counted from 0. All of the
	// workflow's tests run if it is absent.
	Test *int `json:"test"`
	// Repeat is how many times each test runs, 1 if left out. A model can
	// answer differently each time, so one run of a test is one sample.
	Repeat int `json:"repeat"`
}

// maxRepeat is the most times the page may ask for each test.
const maxRepeat = 20

// repeats is how many times a request asks for each test, or why it cannot.
func (s *Server) repeats(req testRequest) (int, error) {
	switch {
	case req.Repeat == 0 || req.Repeat == 1:
		return 1, nil
	case req.Repeat < 0 || req.Repeat > maxRepeat:
		return 0, fmt.Errorf("A test runs from 1 to %d times.", maxRepeat)
	case s.opts.Replay:
		return 0, errors.New("The server answers with recorded answers, which are the same every time, so running a test again shows nothing. Start it without --replay to run tests several times.")
	}
	return req.Repeat, nil
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

	repeat, err := s.repeats(req)
	if err != nil {
		return http.StatusUnprocessableEntity, fail("%v", err)
	}
	// Each test's runs come one after another, then the next test's.
	round := newID()
	var runs []started
	for _, t := range tests {
		for range repeat {
			runs = append(runs, started{Run: newID(), Test: testName(t)})
		}
	}
	s.running.Add(1)
	go func() {
		defer s.running.Done()
		for j, t := range tests {
			for k := range repeat {
				s.runTest(runs[j*repeat+k].Run, round, w, t)
			}
		}
	}()
	return http.StatusAccepted, map[string]any{"runs": runs, "round": round, "repeat": repeat}
}

func testName(t workflow.Test) string {
	if t.Name != "" {
		return t.Name
	}
	return t.Fixture
}

// runTest runs one test to completion and records it as a run. The run
// failed if a step failed or if the test expected something else.
func (s *Server) runTest(id, round string, w *workflow.Workflow, t workflow.Test) Run {
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()

	run := Run{
		ID: id, Workflow: w.Name, Source: "test", Test: testName(t), Round: round, Commit: s.commit(),
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
			run.Tokens.Add(r.Tokens)
			s.history.put(run)
		},
	}
	res, err := eng.Test(ctx, w, t)
	run.Response = res.Response
	s.finishTest(&run, res, err)
	s.opts.Log("%s %s test %q %s in %s%s", id, w.Name, run.Test, run.Status, time.Duration(run.Millis)*time.Millisecond, tokensOf(run.Tokens))
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
	repeat, err := s.repeats(req)
	if err != nil {
		return http.StatusUnprocessableEntity, fail("%v", err)
	}
	round := newID()
	var runs []started
	for j := range tests {
		for range repeat {
			runs = append(runs, started{Run: newID(), Test: names[j]})
		}
	}
	s.running.Add(1)
	go func() {
		defer s.running.Done()
		for j, t := range tests {
			for k := range repeat {
				s.runComponentTest(runs[j*repeat+k].Run, round, c, t, names[j])
			}
		}
	}()
	return http.StatusAccepted, map[string]any{"runs": runs, "round": round, "repeat": repeat}
}

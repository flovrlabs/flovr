package workflow

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Duration is a length of time written as Go writes one, such as 30s, 2m or
// 1m30s.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%q is not a length of time, such as 30s or 2m", s)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) String() string { return time.Duration(d).String() }

// Failures a step can be told to try again after. Each is one that a second
// try may not meet.
const (
	RateLimit     = "rate-limit"     // a model service or server answered 429
	Unavailable   = "unavailable"    // it answered 5xx, or could not be reached
	TimedOut      = "timeout"        // the try took longer than the step's timeout
	InvalidAnswer = "invalid-answer" // a model's answer was empty, not JSON, or did not fit the schema
)

// Failures lists every kind of failure that retry can name.
var Failures = []string{RateLimit, Unavailable, TimedOut, InvalidAnswer}

// Retry says how often a step tries again after a failure, and after which.
// It is written as a number of tries after the first, retry: 2, or in full:
//
//	retry:
//	  times: 2
//	  wait: 2s
//	  on: [rate-limit, unavailable]
type Retry struct {
	Times int      `yaml:"times"`
	Wait  Duration `yaml:"wait"` // before the first try again, doubled for each one after; 1s if left out
	On    []string `yaml:"on"`   // the failures to try again after; every one of Failures if left out
}

func (r *Retry) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&r.Times)
	}
	type plain Retry
	return n.Decode((*plain)(r))
}

// After reports whether the step tries again after a failure of the kind
// given, one of Failures.
func (r *Retry) After(failure string) bool {
	if r == nil || failure == "" {
		return false
	}
	return len(r.On) == 0 || slices.Contains(r.On, failure)
}

// Pause is how long to wait before try number n, counted from 2.
func (r *Retry) Pause(n int) time.Duration {
	wait := time.Duration(r.Wait)
	if wait == 0 {
		wait = time.Second
	}
	for i := 2; i < n && wait < time.Minute; i++ {
		wait *= 2
	}
	return min(wait, time.Minute)
}

// Limits on what a step may ask for. A run on a server is stopped after five
// minutes whatever its steps say.
const (
	maxTimeout = time.Hour
	maxTimes   = 10
	maxWait    = 5 * time.Minute
)

// checkLimits reports what is wrong with a step's timeout and retry.
func checkLimits(s Step) []error {
	var errs []error
	if t := s.Timeout; t != nil && (*t <= 0 || time.Duration(*t) > maxTimeout) {
		errs = append(errs, fmt.Errorf("timeout: must be more than 0 and at most %s; leave it out for no timeout", maxTimeout))
	}
	if r := s.Retry; r != nil {
		if r.Times < 1 || r.Times > maxTimes {
			errs = append(errs, fmt.Errorf("retry: times must be from 1 to %d", maxTimes))
		}
		if r.Wait < 0 || time.Duration(r.Wait) > maxWait {
			errs = append(errs, fmt.Errorf("retry: wait must be at most %s", maxWait))
		}
		for _, f := range r.On {
			if !slices.Contains(Failures, f) {
				errs = append(errs, fmt.Errorf("retry: on: %q is not one of %s", f, strings.Join(Failures, ", ")))
			}
		}
		if slices.Contains(r.On, TimedOut) && s.Timeout == nil {
			errs = append(errs, errors.New("retry: on: timeout needs the step to have a timeout"))
		}
		if slices.Contains(r.On, InvalidAnswer) && s.AI == nil && s.Use == "" {
			errs = append(errs, errors.New("retry: on: invalid-answer is for ai steps and steps that use a component"))
		}
	}
	return errs
}

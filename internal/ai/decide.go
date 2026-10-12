package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"slices"
	"strconv"
	"time"
)

// QuestionKind is the form of answer a question asks for.
type QuestionKind string

const (
	YesNo  QuestionKind = "yesno"
	Choice QuestionKind = "choice"
	Score  QuestionKind = "score"
)

// Option is one allowed answer to a choice or score question. Meaning tells
// the model when the answer applies, and may be empty.
type Option struct {
	Name    string `json:"name"`
	Meaning string `json:"meaning,omitempty"`
}

// Question is one thing for a decision model to decide.
type Question struct {
	Ask     string
	Kind    QuestionKind
	Options []Option // Choice: in any order. Score: from lowest to highest.
	YesIf   string   // YesNo: optional guidance, given together with NoIf
	NoIf    string
}

// DecideRequest asks a decision model named questions about State, which is
// text or any JSON value.
type DecideRequest struct {
	Model     string
	State     any
	Questions map[string]Question
}

// DecideResponse holds an answer for every question, each in the form that
// workflows see:
//
//	yes/no: {"value": bool, "probability": p}
//	choice: {"value": name, "confidence": c, "probabilities": {name: p, ...}}
//	score:  {"value": name, "score": s, "confidence": c, "probabilities": {name: p, ...}}
//
// A score's value is its most probable level, and its score is the expected
// level counted from 0, so 1.5 lies halfway between the second and third.
type DecideResponse struct {
	Answers     map[string]any
	Model       string
	InputTokens int64
}

// OpenRouterDecisions calls OpenRouter's Decisions API, which is in alpha.
type OpenRouterDecisions struct {
	url    string
	key    string
	client *http.Client
}

// NewOpenRouterDecisions returns a client with its key taken from
// OPENROUTER_API_KEY.
func NewOpenRouterDecisions() *OpenRouterDecisions {
	return &OpenRouterDecisions{
		url:    "https://openrouter.ai/api/alpha/decisions",
		key:    os.Getenv("OPENROUTER_API_KEY"),
		client: &http.Client{Timeout: time.Minute},
	}
}

func (d *OpenRouterDecisions) Decide(ctx context.Context, req DecideRequest) (DecideResponse, error) {
	if d.key == "" {
		return DecideResponse{}, &UnavailableError{errors.New("no OpenRouter credentials found; set OPENROUTER_API_KEY")}
	}

	questions := make(map[string]wireQuestion, len(req.Questions))
	for name, q := range req.Questions {
		questions[name] = toWire(q)
	}
	body, err := json.Marshal(map[string]any{"model": req.Model, "state": req.State, "questions": questions})
	if err != nil {
		return DecideResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url, bytes.NewReader(body))
	if err != nil {
		return DecideResponse{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+d.key)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(httpReq)
	if err != nil {
		return DecideResponse{}, &UnavailableError{err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return DecideResponse{}, &UnavailableError{err}
	}
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		json.Unmarshal(data, &failure)
		return DecideResponse{}, &UnavailableError{statusError("OpenRouter", "OPENROUTER_API_KEY", resp.StatusCode, failure.Error.Message)}
	}

	var got struct {
		Model   string                `json:"model"`
		Answers map[string]wireAnswer `json:"answers"`
		Usage   struct {
			InputTokens int64 `json:"input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		return DecideResponse{}, fmt.Errorf("OpenRouter's response is not the JSON expected: %w", err)
	}
	answers := make(map[string]any, len(req.Questions))
	for name, q := range req.Questions {
		a, ok := got.Answers[name]
		if !ok {
			return DecideResponse{}, fmt.Errorf("the model did not answer the question %q", name)
		}
		answers[name], err = fromWire(q, a)
		if err != nil {
			return DecideResponse{}, fmt.Errorf("the model's answer to %q: %w", name, err)
		}
	}
	return DecideResponse{Answers: answers, Model: got.Model, InputTokens: got.Usage.InputTokens}, nil
}

// The Decisions API calls a yes/no question "noul", a question's text its
// instructions, and the meanings of its answers its criteria.
type wireQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

type wireAnswer struct {
	Type          string             `json:"type"`
	Noul          float64            `json:"noul"`
	Choice        string             `json:"choice"`
	Score         float64            `json:"score"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

func toWire(q Question) wireQuestion {
	switch q.Kind {
	case Choice:
		return wireQuestion{Type: "choice", Instructions: q.Ask, Criteria: orderedCriteria(q.Options)}
	case Score:
		// Levels are sent as an ordered list, so each carries its own name.
		levels := make([]string, len(q.Options))
		for i, o := range q.Options {
			levels[i] = o.Name
			if o.Meaning != "" {
				levels[i] += ": " + o.Meaning
			}
		}
		return wireQuestion{Type: "score", Instructions: q.Ask, Criteria: levels}
	}
	w := wireQuestion{Type: "noul", Instructions: q.Ask}
	if q.YesIf != "" || q.NoIf != "" {
		w.Criteria = map[string]string{"true": q.YesIf, "false": q.NoIf}
	}
	return w
}

// orderedCriteria marshals as a JSON object of option name to meaning, in the
// order the options were written. A Go map would sort them.
type orderedCriteria []Option

func (c orderedCriteria) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, o := range c {
		if i > 0 {
			buf.WriteByte(',')
		}
		name, _ := json.Marshal(o.Name)
		buf.Write(name)
		buf.WriteByte(':')
		if o.Meaning == "" {
			buf.WriteString("null")
			continue
		}
		meaning, _ := json.Marshal(o.Meaning)
		buf.Write(meaning)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func fromWire(q Question, a wireAnswer) (map[string]any, error) {
	want := map[QuestionKind]string{YesNo: "noul", Choice: "choice", Score: "score"}[q.Kind]
	if a.Type != want {
		return nil, fmt.Errorf("is of type %q, not %q", a.Type, want)
	}

	switch q.Kind {
	case Choice:
		if !slices.ContainsFunc(q.Options, func(o Option) bool { return o.Name == a.Choice }) {
			return nil, fmt.Errorf("%q is not one of the options", a.Choice)
		}
		out := map[string]any{"value": a.Choice, "confidence": a.Confidence}
		if len(a.Probabilities) > 0 {
			probabilities := map[string]any{}
			for name, p := range a.Probabilities {
				probabilities[name] = p
			}
			out["probabilities"] = probabilities
		}
		return out, nil

	case Score:
		out := map[string]any{"score": a.Score, "confidence": a.Confidence}
		// The API numbers the levels from 0 in the order they were sent.
		best := min(max(int(math.Round(a.Score)), 0), len(q.Options)-1)
		if len(a.Probabilities) > 0 {
			probabilities := map[string]any{}
			for i, o := range q.Options {
				p, ok := a.Probabilities[strconv.Itoa(i)]
				if !ok {
					return nil, fmt.Errorf("has no probability for level %q", o.Name)
				}
				probabilities[o.Name] = p
				if p > a.Probabilities[strconv.Itoa(best)] {
					best = i
				}
			}
			out["probabilities"] = probabilities
		}
		out["value"] = q.Options[best].Name
		return out, nil
	}
	return map[string]any{"value": a.Noul >= 0.5, "probability": a.Noul}, nil
}

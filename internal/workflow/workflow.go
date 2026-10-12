// Package workflow reads and validates workflow files.
package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	"aia/internal/ai"
	"aia/internal/expr"
)

// What a test can expect of a whole step, as in `escalate: skipped`.
const (
	Called  = "called"
	Skipped = "skipped"
)

// FileName is the file Load looks for when given a directory.
const FileName = "workflow.yaml"

type Workflow struct {
	Name    string   `yaml:"name"`
	Trigger *Trigger `yaml:"trigger"`
	Steps   []Step   `yaml:"steps"`
	Tests   []Test   `yaml:"tests"`

	// Path is the workflow file. Paths inside the file are relative to its
	// directory.
	Path string     `yaml:"-"`
	root *yaml.Node // parsed document, kept for line numbers
}

type Trigger struct {
	Webhook string `yaml:"webhook"`
	Cron    string `yaml:"cron"`
	Manual  bool   `yaml:"manual"`
}

type Step struct {
	ID     string      `yaml:"id"`
	When   string      `yaml:"when"`
	AI     *AIStep     `yaml:"ai"`
	Decide *DecideStep `yaml:"decide"`
	HTTP   *HTTPStep   `yaml:"http"`
}

// Kind names the step's type, or is empty if the step has none.
func (s Step) Kind() string {
	switch {
	case s.AI != nil:
		return "ai"
	case s.Decide != nil:
		return "decide"
	case s.HTTP != nil:
		return "http"
	}
	return ""
}

// DecideStep asks a decision model typed questions about its input. The model
// answers with probabilities instead of writing text.
type DecideStep struct {
	Model     string              `yaml:"model"`
	Input     any                 `yaml:"input"`
	Questions map[string]Question `yaml:"questions"`
}

// Question is a yes/no question unless it has choice or score.
type Question struct {
	Ask    string  `yaml:"ask"`
	Choice Options `yaml:"choice"`
	Score  Levels  `yaml:"score"`
	YesIf  string  `yaml:"yes_if"`
	NoIf   string  `yaml:"no_if"`
}

// Spec is the question in the form a decision model takes.
func (q Question) Spec() ai.Question {
	switch {
	case q.Choice != nil:
		return ai.Question{Ask: q.Ask, Kind: ai.Choice, Options: q.Choice}
	case q.Score != nil:
		return ai.Question{Ask: q.Ask, Kind: ai.Score, Options: q.Score}
	}
	return ai.Question{Ask: q.Ask, Kind: ai.YesNo, YesIf: q.YesIf, NoIf: q.NoIf}
}

// Options are the answers to a choice question in the order written: a map of
// name to meaning, or a list of names and name: meaning pairs.
type Options []ai.Option

func (o *Options) UnmarshalYAML(n *yaml.Node) (err error) {
	*o, err = decodeOptions(n, true)
	return err
}

// Levels are the answers to a score question from lowest to highest. They are
// always a list, because their order matters and the order of a map does not.
type Levels []ai.Option

func (l *Levels) UnmarshalYAML(n *yaml.Node) (err error) {
	*l, err = decodeOptions(n, false)
	return err
}

func decodeOptions(n *yaml.Node, mapAllowed bool) ([]ai.Option, error) {
	opts := []ai.Option{}
	pair := func(name, meaning *yaml.Node) error {
		if name.Kind != yaml.ScalarNode || meaning.Kind != yaml.ScalarNode {
			return fmt.Errorf("line %d: expected name: meaning", name.Line)
		}
		o := ai.Option{Name: name.Value, Meaning: meaning.Value}
		if meaning.Tag == "!!null" {
			o.Meaning = ""
		}
		opts = append(opts, o)
		return nil
	}

	switch {
	case n.Kind == yaml.SequenceNode:
		for _, item := range n.Content {
			switch {
			case item.Kind == yaml.ScalarNode:
				opts = append(opts, ai.Option{Name: item.Value})
			case item.Kind == yaml.MappingNode && len(item.Content) == 2:
				if err := pair(item.Content[0], item.Content[1]); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("line %d: expected a name or one name: meaning pair", item.Line)
			}
		}
	case n.Kind == yaml.MappingNode && mapAllowed:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if err := pair(n.Content[i], n.Content[i+1]); err != nil {
				return nil, err
			}
		}
	case n.Kind == yaml.MappingNode:
		return nil, fmt.Errorf("line %d: score must be a list, lowest level first, because a map does not keep its order", n.Line)
	default:
		return nil, fmt.Errorf("line %d: expected a list of names or name: meaning pairs", n.Line)
	}
	return opts, nil
}

type AIStep struct {
	Model  string `yaml:"model"`
	Prompt string `yaml:"prompt"`
	Input  any    `yaml:"input"`
	Output string `yaml:"output"`
}

type HTTPStep struct {
	Method  string            `yaml:"method"`
	URL     string            `yaml:"url"`
	Headers map[string]string `yaml:"headers"`
	Body    any               `yaml:"body"`
}

type Test struct {
	Name    string         `yaml:"name"`
	Fixture string         `yaml:"fixture"`
	Expect  map[string]any `yaml:"expect"`
}

// Problem is one thing wrong with a workflow file.
type Problem struct {
	Line int
	Msg  string
}

// Problems is the error Load returns for a file that was read but is not a
// valid workflow.
type Problems struct {
	Path  string
	Items []Problem
}

func (p *Problems) Error() string {
	var sb strings.Builder
	for _, it := range p.Items {
		fmt.Fprintf(&sb, "%s:%d: %s\n", p.Path, it.Line, it.Msg)
	}
	noun := "problems"
	if len(p.Items) == 1 {
		noun = "problem"
	}
	fmt.Fprintf(&sb, "%d %s", len(p.Items), noun)
	return sb.String()
}

// Load reads the workflow at path, which is a workflow file or a directory
// containing one, and checks everything that can be checked without running
// it. The error is a *Problems when the file itself is at fault.
func Load(path string) (*Workflow, error) {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		path = filepath.Join(path, FileName)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	w := &Workflow{Path: path}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(w); err != nil {
		return nil, &Problems{Path: path, Items: yamlProblems(err)}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err == nil && len(doc.Content) > 0 {
		w.root = doc.Content[0]
	}

	c := &checker{w: w}
	c.run()
	if len(c.items) > 0 {
		sort.SliceStable(c.items, func(i, j int) bool { return c.items[i].Line < c.items[j].Line })
		return nil, &Problems{Path: path, Items: c.items}
	}
	return w, nil
}

// Resolve turns a path written in the workflow file into one usable from the
// current directory.
func (w *Workflow) Resolve(rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(filepath.Dir(w.Path), rel)
}

// Schema loads the JSON Schema at rel, both compiled for validation and as the
// plain document.
func (w *Workflow) Schema(rel string) (*jsonschema.Schema, map[string]any, error) {
	path := w.Resolve(rel)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("not a JSON object: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	compiled, err := jsonschema.NewCompiler().Compile(abs)
	if err != nil {
		return nil, nil, err
	}
	return compiled, doc, nil
}

// Fixture reads the JSON file at rel, as used for a trigger event.
func (w *Workflow) Fixture(rel string) (any, error) {
	data, err := os.ReadFile(w.Resolve(rel))
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", rel, err)
	}
	return v, nil
}

// EnvVars lists the environment variables the workflow's expressions read as
// env.NAME.
func (w *Workflow) EnvVars() []string {
	var names []string
	for _, s := range w.Steps {
		for _, src := range s.expressions() {
			names = append(names, expr.EnvVars(src)...)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// Reads lists what step i's expressions refer to among the trigger and the
// steps before it, in the order those are defined.
func (w *Workflow) Reads(i int) []string {
	var mentioned []string
	for _, src := range w.Steps[i].expressions() {
		mentioned = append(mentioned, expr.Names(src)...)
	}
	var reads []string
	if slices.Contains(mentioned, "trigger") {
		reads = append(reads, "trigger")
	}
	for _, earlier := range w.Steps[:i] {
		if slices.Contains(mentioned, earlier.ID) {
			reads = append(reads, earlier.ID)
		}
	}
	return reads
}

// expressions returns every expression the step contains: its condition and
// the inside of each ${...} placeholder.
func (s Step) expressions() []string {
	var exprs []string
	if s.When != "" {
		exprs = append(exprs, s.When)
	}
	var templated []any
	switch {
	case s.AI != nil:
		templated = []any{s.AI.Input}
	case s.Decide != nil:
		templated = []any{s.Decide.Input}
	case s.HTTP != nil:
		templated = []any{s.HTTP.URL, s.HTTP.Body}
		for _, value := range s.HTTP.Headers {
			templated = append(templated, value)
		}
	}
	for _, v := range templated {
		exprs = append(exprs, expr.Placeholders(v)...)
	}
	return exprs
}

// line returns the source line of the value at path (map keys and list
// indexes), falling back to the nearest enclosing value that exists.
func (w *Workflow) line(path ...any) int {
	n, line := w.root, 1
	for _, p := range path {
		if n == nil {
			break
		}
		var next *yaml.Node
		switch p := p.(type) {
		case string:
			if n.Kind == yaml.MappingNode {
				for i := 0; i+1 < len(n.Content); i += 2 {
					if n.Content[i].Value == p {
						next, line = n.Content[i+1], n.Content[i].Line
						break
					}
				}
			}
		case int:
			if n.Kind == yaml.SequenceNode && p < len(n.Content) {
				next, line = n.Content[p], n.Content[p].Line
			}
		}
		n = next
	}
	return line
}

var (
	unknownFieldRE = regexp.MustCompile(`^line (\d+): field (\S+) not found in type \S+$`)
	lineRE         = regexp.MustCompile(`^(?:yaml: )?line (\d+): (.*)$`)
)

// yamlProblems rewrites the YAML decoder's errors, which name Go types, as
// problems that name lines and fields.
func yamlProblems(err error) []Problem {
	msgs := []string{err.Error()}
	var te *yaml.TypeError
	if errors.As(err, &te) {
		msgs = te.Errors
	}
	var items []Problem
	for _, msg := range msgs {
		if m := unknownFieldRE.FindStringSubmatch(msg); m != nil {
			line, _ := strconv.Atoi(m[1])
			items = append(items, Problem{line, fmt.Sprintf("unknown field %q", m[2])})
		} else if m := lineRE.FindStringSubmatch(msg); m != nil {
			line, _ := strconv.Atoi(m[1])
			items = append(items, Problem{line, m[2]})
		} else {
			items = append(items, Problem{1, strings.TrimPrefix(msg, "yaml: ")})
		}
	}
	return items
}

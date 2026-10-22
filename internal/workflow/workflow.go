// Package workflow reads and validates workflow files.
package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	"github.com/flovrlabs/flovr/internal/ai"
	"github.com/flovrlabs/flovr/internal/expr"
)

// What a test can expect of a whole step, as in `escalate: skipped`.
const (
	Called  = "called"
	Skipped = "skipped"
)

// FileName is the file Load looks for when given a directory.
const FileName = "workflow.yaml"

// Format is the version of the file format this flovr reads. A file says
// which it is written in as flovr: 1; one that says nothing is in format 1.
const Format = 1

type Workflow struct {
	Flovr   int      `yaml:"flovr"` // the format the file is written in
	Name    string   `yaml:"name"`
	Trigger *Trigger `yaml:"trigger"`
	Steps   []Step   `yaml:"steps"`
	Respond *Respond `yaml:"respond"`
	Tests   []Test   `yaml:"tests"`

	// Path is the workflow file. Paths inside the file are relative to its
	// directory.
	Path    string              `yaml:"-"`
	root    *yaml.Node          // parsed document, kept for line numbers
	outputs map[string][]string // what can be read from each step, by its id
}

type Trigger struct {
	Webhook string `yaml:"webhook"`
	Cron    string `yaml:"cron"`
	Manual  bool   `yaml:"manual"`

	// Input is the path of a JSON Schema that the body of a webhook request
	// must match. A request whose body does not is refused and runs nothing.
	Input string `yaml:"input"`
}

type Step struct {
	ID     string      `yaml:"id"`
	When   string      `yaml:"when"`
	AI     *AIStep     `yaml:"ai"`
	Decide *DecideStep `yaml:"decide"`
	HTTP   *HTTPStep   `yaml:"http"`

	// Use is the path of a component that the step runs, and With the input
	// it gives the component.
	Use  string         `yaml:"use"`
	With map[string]any `yaml:"with"`

	// Timeout limits each try of the step, and Retry says when it tries
	// again after a failure.
	Timeout *Duration `yaml:"timeout"`
	Retry   *Retry    `yaml:"retry"`

	component *Component // what Use names, once the workflow is loaded
}

// Kind names the step's type, or is empty if the step has none. A step that
// uses a component is of the component's type.
func (s Step) Kind() string {
	switch {
	case s.AI != nil:
		return "ai"
	case s.Decide != nil:
		return "decide"
	case s.HTTP != nil:
		return "http"
	case s.component != nil:
		return s.component.Kind()
	}
	return ""
}

// Component is the component the step uses, or nil if it uses none.
func (s Step) Component() *Component { return s.component }

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
	return Parse(path, data)
}

// Parse is Load for content that is already at hand: data is checked as the
// workflow file at path, which need not hold it yet. The files that data
// refers to are looked up from path's directory.
func Parse(path string, data []byte) (*Workflow, error) { return ParseWith(path, data, nil) }

// Overlay holds content for component files and schema files, by path, to be
// used in place of what the files hold. It lets a change to such a file be
// checked, together with everything that uses it, before it is written.
type Overlay map[string][]byte

// ParseWith is Parse with the component files in files read from there.
func ParseWith(path string, data []byte, files Overlay) (*Workflow, error) {
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

	c := &checker{path: path, root: w.root, w: w, files: files}
	c.run()
	if len(c.items) > 0 {
		sort.SliceStable(c.items, func(i, j int) bool { return c.items[i].Line < c.items[j].Line })
		return nil, &Problems{Path: path, Items: c.items}
	}
	w.outputs = map[string][]string{}
	for _, s := range w.Steps {
		w.outputs[s.ID] = c.shapes[s.ID].paths()
	}
	return w, nil
}

// Resolve turns a path written in the workflow file into one usable from the
// current directory.
func (w *Workflow) Resolve(rel string) string { return resolve(w.Path, rel) }

// Schema loads the JSON Schema at rel, both compiled for validation and as the
// plain document.
func (w *Workflow) Schema(rel string) (*jsonschema.Schema, map[string]any, error) {
	return loadSchema(w.Path, rel, nil)
}

// Fixture reads the JSON file at rel, as used for a trigger event.
func (w *Workflow) Fixture(rel string) (any, error) { return loadJSON(w.Path, rel) }

// resolve turns rel, a path written in file, into one usable from the current
// directory.
func resolve(file, rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(filepath.Dir(file), rel)
}

func loadSchema(file, rel string, files Overlay) (*jsonschema.Schema, map[string]any, error) {
	path := resolve(file, rel)
	data, given := files[path]
	if !given {
		var err error
		if data, err = os.ReadFile(path); err != nil {
			return nil, nil, err
		}
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("not a JSON object: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	// The compiler takes a location, in which a # or a space in a folder's
	// name would mean something else unless it is written as a URL.
	location := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	compiler := jsonschema.NewCompiler()
	if given {
		parsed, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			return nil, nil, err
		}
		if err := compiler.AddResource(location.String(), parsed); err != nil {
			return nil, nil, err
		}
	}
	compiled, err := compiler.Compile(location.String())
	if err != nil {
		return nil, nil, err
	}
	return compiled, doc, nil
}

func loadJSON(file, rel string) (any, error) {
	data, err := os.ReadFile(resolve(file, rel))
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", rel, err)
	}
	return v, nil
}

// EnvVars lists the environment variables read as env.NAME by the workflow's
// expressions and by those of the components it uses.
func (w *Workflow) EnvVars() []string {
	var names []string
	for _, s := range w.Steps {
		for _, src := range s.expressions() {
			names = append(names, expr.EnvVars(src)...)
		}
		if s.component != nil {
			names = append(names, s.component.EnvVars()...)
		}
	}
	for _, src := range w.Respond.expressions() {
		names = append(names, expr.EnvVars(src)...)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// Files lists the files, besides the workflow file, that the workflow was
// built from: those of the components it uses.
func (w *Workflow) Files() []string {
	var paths []string
	for _, s := range w.Steps {
		if s.component != nil {
			paths = append(paths, s.component.Files()...)
		}
	}
	slices.Sort(paths)
	return slices.Compact(paths)
}

// Outputs lists what can be read from the step with the given id once it has
// run, as the fields to put behind its id, such as "category.value". It is
// empty where nothing is known of what the step puts out.
func (w *Workflow) Outputs(id string) []string { return w.outputs[id] }

// Reads lists what step i's expressions refer to among the trigger and the
// steps before it, in the order those are defined.
func (w *Workflow) Reads(i int) []string {
	return reads(w.Steps[i].expressions(), "trigger", w.Steps[:i])
}

// RespondReads lists what the answer to the caller refers to among the
// trigger and the steps, in the order those are defined.
func (w *Workflow) RespondReads() []string {
	return reads(w.Respond.expressions(), "trigger", w.Steps)
}

// reads lists which of given, the name of what steps are handed, and the
// steps the expressions refer to. It is never nil.
func reads(exprs []string, given string, steps []Step) []string {
	var mentioned []string
	for _, src := range exprs {
		mentioned = append(mentioned, expr.Names(src)...)
	}
	out := []string{}
	if slices.Contains(mentioned, given) {
		out = append(out, given)
	}
	for _, step := range steps {
		if slices.Contains(mentioned, step.ID) {
			out = append(out, step.ID)
		}
	}
	return out
}

// expressions returns every expression the step contains: its condition and
// the inside of each ${...} placeholder.
func (s Step) expressions() []string {
	var exprs []string
	if s.When != "" {
		exprs = append(exprs, s.When)
	}
	exprs = append(exprs, placeholders(s.AI, s.Decide, s.HTTP)...)
	return append(exprs, expr.Placeholders(s.With)...)
}

// placeholders returns the inside of each ${...} in whichever of the three
// kinds of work is given.
func placeholders(ai *AIStep, decide *DecideStep, http *HTTPStep) []string {
	var templated []any
	switch {
	case ai != nil:
		templated = []any{ai.Input}
	case decide != nil:
		templated = []any{decide.Input}
	case http != nil:
		templated = []any{http.URL, http.Body}
		for _, value := range http.Headers {
			templated = append(templated, value)
		}
	}
	var exprs []string
	for _, v := range templated {
		exprs = append(exprs, expr.Placeholders(v)...)
	}
	return exprs
}

// Writable reports whether the text at path, given as map keys and list
// indexes, can be changed in the file without rewriting anything around it.
// That takes a key of a mapping written in indented form, not inline as {...},
// whose value is plain text or which the mapping does not have yet.
func (w *Workflow) Writable(path ...any) bool { return writable(w.root, path...) }

func writable(n *yaml.Node, path ...any) bool {
	for i, p := range path {
		if n == nil {
			return false
		}
		var next *yaml.Node
		switch p := p.(type) {
		case string:
			if n.Kind != yaml.MappingNode {
				return false
			}
			for j := 0; j+1 < len(n.Content); j += 2 {
				if n.Content[j].Value == p {
					next = n.Content[j+1]
					break
				}
			}
			if i == len(path)-1 {
				return n.Style&yaml.FlowStyle == 0 && (next == nil || next.Kind == yaml.ScalarNode)
			}
		case int:
			if n.Kind == yaml.SequenceNode && p >= 0 && p < len(n.Content) {
				next = n.Content[p]
			}
		}
		n = next
	}
	return false
}

// lineOf returns the source line of the value at path (map keys and list
// indexes) below root, falling back to the nearest enclosing value that
// exists.
func lineOf(root *yaml.Node, path ...any) int {
	n, line := root, 1
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

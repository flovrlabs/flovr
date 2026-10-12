package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	"aia/internal/expr"
)

// ComponentFile is the file LoadComponent looks for when given a directory.
const ComponentFile = "component.yaml"

// Component is a step written once, in a file of its own, for the steps of
// workflows to use with different input. Its expressions see that input, as
// input.NAME, and env. They see nothing of the workflow that uses it, so a
// component does the same wherever it is used.
type Component struct {
	Name   string          `yaml:"name"`
	Input  Params          `yaml:"input"`
	AI     *AIStep         `yaml:"ai"`
	Decide *DecideStep     `yaml:"decide"`
	HTTP   *HTTPStep       `yaml:"http"`
	Tests  []ComponentTest `yaml:"tests"`

	// Path is the component file. Paths inside the file are relative to its
	// directory.
	Path string     `yaml:"-"`
	root *yaml.Node // parsed document, kept for line numbers
}

// ComponentTest runs a component on its own. The keys of Expect are
// expressions over input and output, whose values are compared.
type ComponentTest struct {
	Name string `yaml:"name"`
	// Input holds the values to give the component, or is the path of a JSON
	// file that holds them.
	Input  any            `yaml:"input"`
	Expect map[string]any `yaml:"expect"`
}

// Kind names what the component does: "ai", "decide" or "http".
func (c *Component) Kind() string {
	return Step{AI: c.AI, Decide: c.Decide, HTTP: c.HTTP}.Kind()
}

// Resolve turns a path written in the component file into one usable from
// the current directory.
func (c *Component) Resolve(rel string) string { return resolve(c.Path, rel) }

// Schema loads the JSON Schema at rel, both compiled for validation and as
// the plain document.
func (c *Component) Schema(rel string) (*jsonschema.Schema, map[string]any, error) {
	return loadSchema(c.Path, rel)
}

// EnvVars lists the environment variables the component's expressions read as
// env.NAME.
func (c *Component) EnvVars() []string {
	var names []string
	for _, src := range placeholders(c.AI, c.Decide, c.HTTP) {
		names = append(names, expr.EnvVars(src)...)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// Values returns the input a test gives the component.
func (c *Component) Values(t ComponentTest) (map[string]any, error) {
	v := t.Input
	if rel, isPath := v.(string); isPath {
		loaded, err := loadJSON(c.Path, rel)
		if err != nil {
			return nil, err
		}
		v = loaded
	}
	values, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("input must hold a value for each of the component's inputs, by name")
	}
	return values, nil
}

// Param is one value a component takes as input.
type Param struct {
	Name string
	Type string // one of ParamTypes
}

// ParamTypes are the types an input can be declared to have.
var ParamTypes = []string{"string", "number", "boolean", "object", "list", "any"}

// Params are a component's inputs in the order written, as name: type.
type Params []Param

func (p *Params) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: input must list what the component takes, as name: type", n.Line)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		name, kind := n.Content[i], n.Content[i+1]
		if kind.Kind != yaml.ScalarNode {
			return fmt.Errorf("line %d: the type of %s must be one of %s", kind.Line, name.Value, strings.Join(ParamTypes, ", "))
		}
		*p = append(*p, Param{Name: name.Value, Type: kind.Value})
	}
	return nil
}

// Check reports the first way in which values are not what the component
// takes: one missing, one too many, or one of the wrong type.
func (p Params) Check(values map[string]any) error {
	for _, param := range p {
		v, given := values[param.Name]
		if !given {
			return fmt.Errorf("%s is missing", param.Name)
		}
		if got := typeOf(v); param.Type != "any" && got != param.Type {
			return fmt.Errorf("%s must be %s, but is %s", param.Name, article(param.Type), article(got))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(values)) {
		if !slices.ContainsFunc(p, func(param Param) bool { return param.Name == name }) {
			return fmt.Errorf("%s is not one of the inputs", name)
		}
	}
	return nil
}

// typeOf names the type of a value as an input's type would be declared.
func typeOf(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case int, int64, uint64, float64:
		return "number"
	case map[string]any:
		return "object"
	case []any:
		return "list"
	case nil:
		return "null"
	}
	return fmt.Sprintf("%T", v)
}

func article(kind string) string {
	switch kind {
	case "null":
		return "empty"
	case "object":
		return "an object"
	}
	return "a " + kind
}

// IsComponent reports whether path is a component file, or a directory that
// holds one and no workflow file.
func IsComponent(path string) bool {
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return filepath.Base(path) == ComponentFile
	}
	if _, err := os.Stat(filepath.Join(path, FileName)); err == nil {
		return false
	}
	_, err := os.Stat(filepath.Join(path, ComponentFile))
	return err == nil
}

// LoadComponent reads the component at path, which is a component file or a
// directory containing one, and checks everything that can be checked without
// running it. The error is a *Problems when the file itself is at fault.
func LoadComponent(path string) (*Component, error) {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		path = filepath.Join(path, ComponentFile)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	comp := &Component{Path: path}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(comp); err != nil {
		return nil, &Problems{Path: path, Items: yamlProblems(err)}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err == nil && len(doc.Content) > 0 {
		comp.root = doc.Content[0]
	}

	c := &checker{path: path, root: comp.root}
	c.component(comp)
	if len(c.items) > 0 {
		sort.SliceStable(c.items, func(i, j int) bool { return c.items[i].Line < c.items[j].Line })
		return nil, &Problems{Path: path, Items: c.items}
	}
	return comp, nil
}

// component checks a component file.
func (c *checker) component(comp *Component) {
	label := fmt.Sprintf("component %q", comp.Name)
	if comp.Name == "" {
		label = "component"
		c.addf(c.line("name"), "name is required")
	}

	seen := map[string]bool{}
	for _, p := range comp.Input {
		line := c.line("input", p.Name)
		switch {
		case !idRE.MatchString(p.Name):
			c.addf(line, "%s: the input %q may contain only letters, digits and underscores, and must not start with a digit", label, p.Name)
		case seen[p.Name]:
			c.addf(line, "%s: the input %q is listed twice", label, p.Name)
		case !slices.Contains(ParamTypes, p.Type):
			c.addf(line, "%s: the type of input %q must be one of %s", label, p.Name, strings.Join(ParamTypes, ", "))
		}
		seen[p.Name] = true
	}

	env, err := expr.Vars("input")
	if err != nil {
		c.addf(1, "%s: %v", label, err)
		return
	}
	kinds := 0
	for _, present := range []bool{comp.AI != nil, comp.Decide != nil, comp.HTTP != nil} {
		if present {
			kinds++
		}
	}
	switch {
	case kinds > 1:
		c.addf(1, "%s: a component can have only one of ai, decide, http", label)
	case comp.AI != nil:
		c.ai(nil, label, comp.AI, env)
	case comp.Decide != nil:
		c.decide(nil, label, comp.Decide, env)
	case comp.HTTP != nil:
		c.http(nil, label, comp.HTTP, env)
	default:
		c.addf(1, "%s: needs one of ai, decide, http", label)
	}

	// An input that is read but not declared would be missing at run time.
	var undeclared []string
	for _, src := range placeholders(comp.AI, comp.Decide, comp.HTTP) {
		for _, name := range expr.Fields(src, "input") {
			if !seen[name] && !slices.Contains(undeclared, name) {
				undeclared = append(undeclared, name)
			}
		}
	}
	for _, name := range undeclared {
		c.addf(c.line(comp.Kind()), "%s: reads input.%s, which is not listed under input", label, name)
	}

	results, err := expr.Vars("input", "output")
	if err != nil {
		c.addf(c.line("tests"), "tests: %v", err)
		return
	}
	for i, t := range comp.Tests {
		label := fmt.Sprintf("test %q", t.Name)
		if t.Name == "" {
			label = fmt.Sprintf("test %d", i+1)
		}
		at := c.line("tests", i, "input")
		if t.Input == nil {
			c.addf(at, "%s: input is required", label)
		} else if values, err := comp.Values(t); errors.Is(err, fs.ErrNotExist) {
			c.addf(at, "%s: input file not found: %v", label, t.Input)
		} else if err != nil {
			c.addf(at, "%s: %v", label, err)
		} else if err := comp.Input.Check(values); err != nil {
			c.addf(at, "%s: input: %v", label, err)
		}
		for key := range t.Expect {
			if err := results.CheckExpr(key); err != nil {
				c.addf(c.line("tests", i, "expect", key), "%s: expect.%s: %v", label, key, err)
			}
		}
	}
}

// use checks the step at base, which uses a component, and returns the
// component if it could be loaded.
func (c *checker) use(base []any, label string, s Step, env *expr.Env) *Component {
	comp, err := c.load(s.Use)
	if err != nil {
		c.addf(c.under(base, "use"), "%s: %v", label, err)
		return nil
	}

	for _, p := range comp.Input {
		v, given := s.With[p.Name]
		at := c.under(base, "with", p.Name)
		if !given {
			c.addf(c.under(base, "with"), "%s: with.%s is missing; %s takes it as input", label, p.Name, comp.Name)
		} else if err := env.Check(v); err != nil {
			c.addf(at, "%s: with.%s: %v", label, p.Name, err)
		} else if got, known := writtenType(v); known && p.Type != "any" && got != p.Type {
			c.addf(at, "%s: with.%s must be %s, but is %s", label, p.Name, article(p.Type), article(got))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(s.With)) {
		if !slices.ContainsFunc(comp.Input, func(p Param) bool { return p.Name == name }) {
			c.addf(c.under(base, "with", name), "%s: with.%s is not an input of %s", label, name, comp.Name)
		}
	}
	return comp
}

// writtenType names the type a value written in a workflow file will have
// when the step runs, if that can be told before it runs. It cannot for a
// value that is one ${...} placeholder and nothing else.
func writtenType(v any) (kind string, known bool) {
	if text, ok := v.(string); ok && expr.Single(text) {
		return "", false
	}
	return typeOf(v), true
}

// load returns the component at the path a step names with use.
func (c *checker) load(use string) (*Component, error) {
	path := resolve(c.path, use)
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		path = filepath.Join(path, ComponentFile)
	}
	if comp, ok := c.components[path]; ok {
		return comp, nil
	}
	comp, err := LoadComponent(path)
	var problems *Problems
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("component not found: %s", use)
	case errors.As(err, &problems):
		first := problems.Items[0]
		more := ""
		if n := len(problems.Items) - 1; n > 0 {
			more = fmt.Sprintf(", and %d more", n)
		}
		return nil, fmt.Errorf("the component %s has a problem: %s:%d: %s%s", use, problems.Path, first.Line, first.Msg, more)
	case err != nil:
		return nil, fmt.Errorf("component %s: %v", use, err)
	}
	if c.components == nil {
		c.components = map[string]*Component{}
	}
	c.components[path] = comp
	return comp, nil
}

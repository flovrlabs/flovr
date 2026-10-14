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

	"github.com/simonyosb/flowmaker/internal/expr"
)

// ComponentFile is the file LoadComponent looks for when given a directory.
const ComponentFile = "component.yaml"

// Component is a step written once, in a file of its own, for the steps of
// workflows to use with different input. Its expressions see that input, as
// input.NAME, and env. They see nothing of the workflow that uses it, so a
// component does the same wherever it is used.
//
// A component does one kind of work, ai, decide or http, or is made of
// steps of its own, which may use other components. One made of steps gives
// back what its output says.
type Component struct {
	Name string `yaml:"name"`
	// Description says in a line what the component is for, to whoever
	// chooses among components.
	Description string      `yaml:"description"`
	Input       Params      `yaml:"input"`
	AI          *AIStep     `yaml:"ai"`
	Decide      *DecideStep `yaml:"decide"`
	HTTP        *HTTPStep   `yaml:"http"`

	// Steps run in order, as in a workflow, and Output is what the component
	// gives back once they have: a value that may take values from them.
	Steps  []Step `yaml:"steps"`
	Output any    `yaml:"output"`

	Tests []ComponentTest `yaml:"tests"`

	// Path is the component file. Paths inside the file are relative to its
	// directory.
	Path string `yaml:"-"`
	// Remote is how a step names the component when it is taken from git,
	// as in github.com/acme/services/notify@v1.2, and empty otherwise.
	Remote  string              `yaml:"-"`
	root    *yaml.Node          // parsed document, kept for line numbers
	outputs map[string][]string // what can be read from each of its steps, by id
	gives   []string            // what can be read from what it gives back
}

// Writable reports whether the text at path can be changed in the component
// file without rewriting anything around it, as Workflow.Writable does.
func (c *Component) Writable(path ...any) bool { return writable(c.root, path...) }

// Reads lists what step i of a component made of steps refers to among the
// input and the steps before it, in the order those are defined.
func (c *Component) Reads(i int) []string {
	return reads(c.Steps[i].expressions(), "input", c.Steps[:i])
}

// OutputReads lists what the output of a component made of steps refers to
// among the input and its steps.
func (c *Component) OutputReads() []string {
	return reads(expr.Placeholders(c.Output), "input", c.Steps)
}

// Outputs lists what can be read from the step with the given id of a
// component made of steps, as Workflow.Outputs does.
func (c *Component) Outputs(id string) []string { return c.outputs[id] }

// Gives lists what a step that uses the component can read from it, as the
// fields to put behind the step's id.
func (c *Component) Gives() []string { return c.gives }

// ComponentTest runs a component on its own. The keys of Expect are
// expressions over input and output, whose values are compared. For a
// component made of steps a key can also be a step's id, expecting it to be
// called or skipped, and expressions can read the steps.
type ComponentTest struct {
	Name string `yaml:"name"`
	// Input holds the values to give the component, or is the path of a JSON
	// file that holds them.
	Input  any            `yaml:"input"`
	Expect map[string]any `yaml:"expect"`
}

// Kind names what the component does: "ai", "decide" or "http", or "flow" if
// it is made of steps.
func (c *Component) Kind() string {
	if len(c.Steps) > 0 {
		return "flow"
	}
	return Step{AI: c.AI, Decide: c.Decide, HTTP: c.HTTP}.Kind()
}

// Files lists the component's file and those of the components it uses.
func (c *Component) Files() []string {
	paths := []string{c.Path}
	for _, s := range c.Steps {
		if s.component != nil {
			paths = append(paths, s.component.Files()...)
		}
	}
	return paths
}

// expressions returns every expression the component contains.
func (c *Component) expressions() []string {
	exprs := placeholders(c.AI, c.Decide, c.HTTP)
	for _, s := range c.Steps {
		exprs = append(exprs, s.expressions()...)
	}
	return append(exprs, expr.Placeholders(c.Output)...)
}

// shape is what is known of what the component gives back.
func (c *Component) shape() *shape {
	if len(c.Steps) > 0 {
		return writtenShape(c.Output)
	}
	return outputShape(c.Path, nil, c.AI, c.Decide, c.HTTP)
}

// Resolve turns a path written in the component file into one usable from
// the current directory.
func (c *Component) Resolve(rel string) string { return resolve(c.Path, rel) }

// Schema loads the JSON Schema at rel, both compiled for validation and as
// the plain document.
func (c *Component) Schema(rel string) (*jsonschema.Schema, map[string]any, error) {
	return loadSchema(c.Path, rel, nil)
}

// EnvVars lists the environment variables read as env.NAME by the
// component's expressions and by those of the components it uses.
func (c *Component) EnvVars() []string {
	var names []string
	for _, src := range c.expressions() {
		names = append(names, expr.EnvVars(src)...)
	}
	for _, s := range c.Steps {
		if s.component != nil {
			names = append(names, s.component.EnvVars()...)
		}
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

// ComponentsIn loads the components in dir and below it, ordered by path. It
// leaves out components that are not valid, which no step could use.
func ComponentsIn(dir string) []*Component {
	var found []*Component
	for _, path := range ComponentFiles(dir) {
		if c, err := LoadComponent(path); err == nil {
			found = append(found, c)
		}
	}
	return found
}

// ComponentFiles finds the component files in dir and below it, ordered by
// path. It skips hidden directories and testdata, as Discover does.
func ComponentFiles(dir string) []string {
	var found []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dir && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == ComponentFile {
			found = append(found, p)
		}
		return nil
	})
	return found
}

// LoadComponent reads the component at path, which is a component file or a
// directory containing one, and checks everything that can be checked without
// running it. The error is a *Problems when the file itself is at fault.
func LoadComponent(path string) (*Component, error) { return loadComponent(path, nil, nil) }

// LoadComponentWith is LoadComponent with the component files in files read
// from there.
func LoadComponentWith(path string, files Overlay) (*Component, error) {
	return loadComponent(path, nil, files)
}

// loadComponent is LoadComponent for a component used by those in loading,
// each used by the one before it.
func loadComponent(path string, loading []string, files Overlay) (*Component, error) {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		path = filepath.Join(path, ComponentFile)
	}
	data, given := files[path]
	if !given {
		var err error
		if data, err = os.ReadFile(path); err != nil {
			return nil, err
		}
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

	c := &checker{path: path, root: comp.root, loading: append(slices.Clone(loading), path), files: files, remote: checkoutOf(path)}
	c.component(comp)
	if len(c.items) > 0 {
		sort.SliceStable(c.items, func(i, j int) bool { return c.items[i].Line < c.items[j].Line })
		return nil, &Problems{Path: path, Items: c.items}
	}
	comp.outputs = map[string][]string{}
	for _, s := range comp.Steps {
		comp.outputs[s.ID] = c.shapes[s.ID].paths()
	}
	comp.gives = comp.shape().paths()
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
	// A component from git gets nothing it is not given: env holds the
	// server's secrets.
	if c.remote != "" {
		for _, src := range comp.expressions() {
			for _, name := range expr.Fields(src, "env") {
				c.addf(1, "%s: it comes from git, so it cannot read env.%s; a step that uses it gives it what it needs as input", label, name)
			}
		}
	}
	// Reading a field of an input that is a single value is a mistake. An
	// input that is not listed at all is reported further down.
	given := inputShape(comp.Input)
	given.open = true
	c.shapes = map[string]*shape{"input": given}

	kinds := 0
	for _, present := range []bool{comp.AI != nil, comp.Decide != nil, comp.HTTP != nil, comp.Steps != nil} {
		if present {
			kinds++
		}
	}
	// where is the line of the work the component does.
	where := 1
	var ids []string
	switch {
	case kinds > 1:
		c.addf(1, "%s: a component can have only one of ai, decide, http, steps", label)
		// Its steps are not checked, but tests may still name them.
		for _, s := range comp.Steps {
			ids = append(ids, s.ID)
		}
	case comp.AI != nil:
		where = c.line("ai")
		c.ai(nil, label, comp.AI, env)
	case comp.Decide != nil:
		where = c.line("decide")
		c.decide(nil, label, comp.Decide, env)
	case comp.HTTP != nil:
		where = c.line("http")
		c.http(nil, label, comp.HTTP, env)
	case comp.Steps != nil:
		where = c.line("steps")
		ids = c.flow(comp, label)
	default:
		c.addf(1, "%s: needs one of ai, decide, http, steps", label)
	}
	if comp.Output != nil && comp.Steps == nil {
		c.addf(c.line("output"), "%s: output is for a component made of steps; this one gives back what its %s puts out", label, comp.Kind())
	}
	if comp.Steps == nil {
		c.reads(where, label, comp.expressions(), c.shapes)
	}

	// An input that is read but not declared would be missing at run time.
	var undeclared []string
	for _, src := range comp.expressions() {
		for _, name := range expr.Fields(src, "input") {
			if !seen[name] && !slices.Contains(undeclared, name) {
				undeclared = append(undeclared, name)
			}
		}
	}
	for _, name := range undeclared {
		c.addf(where, "%s: reads input.%s, which is not listed under input", label, name)
	}
	c.shapes["output"] = comp.shape()

	results, err := expr.Vars(append([]string{"input", "output"}, ids...)...)
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
		if rel, isPath := t.Input.(string); isPath && c.outside(rel) {
			c.addf(at, "%s: %s is outside the repository it comes from", label, rel)
		} else if t.Input == nil {
			c.addf(at, "%s: input is required", label)
		} else if values, err := comp.Values(t); errors.Is(err, fs.ErrNotExist) {
			c.addf(at, "%s: input file not found: %v", label, t.Input)
		} else if err != nil {
			c.addf(at, "%s: %v", label, err)
		} else if err := comp.Input.Check(values); err != nil {
			c.addf(at, "%s: input: %v", label, err)
		}
		for key, want := range t.Expect {
			at := c.line("tests", i, "expect", key)
			if slices.Contains(ids, key) {
				if want != Called && want != Skipped {
					c.addf(at, "%s: expect.%s must be %s or %s", label, key, Called, Skipped)
				}
			} else if err := results.CheckExpr(key); err != nil {
				c.addf(at, "%s: expect.%s: %v", label, key, err)
			} else {
				c.reads(at, label+": expect", []string{key}, c.shapes)
			}
		}
	}
}

// flow checks a component made of steps and returns the ids of its steps.
func (c *checker) flow(comp *Component, label string) []string {
	if len(comp.Steps) == 0 {
		c.addf(c.line("steps"), "%s: steps needs at least one step", label)
	}
	ids := c.steps(comp.Steps, "input")

	at := c.line("output")
	if comp.Output == nil {
		c.addf(at, "%s: output is required: it is what the component gives back to the step that uses it", label)
		return ids
	}
	env, err := expr.Vars(append([]string{"input"}, ids...)...)
	if err != nil {
		c.addf(at, "%s: output: %v", label, err)
	} else if err := env.Check(comp.Output); err != nil {
		c.addf(at, "%s: output: %v", label, err)
	} else {
		c.reads(at, label+": output", expr.Placeholders(comp.Output), c.shapes)
	}
	return ids
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

// load returns the component a step names with use: at a path, or in git.
func (c *checker) load(use string) (*Component, error) {
	var path string
	switch {
	case IsRemote(use) && c.remote != "":
		return nil, fmt.Errorf("%s is from git, and a component from git cannot use another one from git", use)
	case IsRemote(use):
		var err error
		if path, err = remoteFile(c.path, use); err != nil {
			return nil, err
		}
	default:
		path = resolve(c.path, use)
		if c.remote != "" && !inside(c.remote, path) {
			return nil, fmt.Errorf("%s is outside the repository this component comes from", use)
		}
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			path = filepath.Join(path, ComponentFile)
		}
	}
	if comp, ok := c.components[path]; ok {
		return comp, nil
	}
	if slices.Contains(c.loading, path) {
		return nil, fmt.Errorf("the component %s would use itself, which never ends", use)
	}
	comp, err := loadComponent(path, c.loading, c.files)
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
	if IsRemote(use) {
		comp.Remote = use
	}
	if c.components == nil {
		c.components = map[string]*Component{}
	}
	c.components[path] = comp
	return comp, nil
}

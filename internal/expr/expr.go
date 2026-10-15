// Package expr evaluates the expressions used in workflow files: bare CEL
// conditions (a step's `when`) and strings with ${...} placeholders.
package expr

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"

	"cel.dev/cel-go/cel"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/types/ref"
	"google.golang.org/protobuf/types/known/structpb"
)

type Env struct{ cel *cel.Env }

// NewEnv returns an environment in which expressions can refer to trigger,
// env, and the outputs of the given steps.
func NewEnv(steps []string) (*Env, error) {
	return Vars(append([]string{"trigger"}, steps...)...)
}

// Vars returns an environment in which expressions can refer to env and to
// values with the given names.
func Vars(names ...string) (*Env, error) {
	opts := []cel.EnvOption{
		cel.Variable("env", cel.MapType(cel.StringType, cel.StringType)),
	}
	for _, name := range names {
		opts = append(opts, cel.Variable(name, cel.DynType))
	}
	e, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, err
	}
	return &Env{e}, nil
}

// CheckCondition reports whether src is a valid expression that yields a bool.
func (e *Env) CheckCondition(src string) error {
	_, t, err := e.compile(src)
	if err != nil {
		return err
	}
	if k := t.Kind(); k != cel.BoolKind && k != cel.DynKind {
		return fmt.Errorf("must be true or false, but is a %s", t)
	}
	return nil
}

func (e *Env) EvalCondition(src string, vars map[string]any) (bool, error) {
	prg, _, err := e.compile(src)
	if err != nil {
		return false, err
	}
	out, _, err := prg.Eval(vars)
	if err != nil {
		return false, err
	}
	b, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("must be true or false, got %v", out.Value())
	}
	return b, nil
}

// CheckExpr reports whether src is a valid expression.
func (e *Env) CheckExpr(src string) error {
	_, _, err := e.compile(src)
	return err
}

// EvalExpr returns the value of the bare expression src.
func (e *Env) EvalExpr(src string, vars map[string]any) (any, error) {
	prg, _, err := e.compile(src)
	if err != nil {
		return nil, err
	}
	out, _, err := prg.Eval(vars)
	if err != nil {
		return nil, err
	}
	return native(out)
}

// Check reports the first invalid ${...} placeholder in v, which may be a
// string or any nesting of maps and lists that contain strings.
func (e *Env) Check(v any) error {
	_, err := e.walk(v, nil, false)
	return err
}

// Eval returns v with every ${...} placeholder replaced by its value. A string
// that is exactly one placeholder keeps the value's type; otherwise values are
// spliced into the surrounding text.
func (e *Env) Eval(v any, vars map[string]any) (any, error) {
	return e.walk(v, vars, true)
}

func (e *Env) EvalString(src string, vars map[string]any) (string, error) {
	v, err := e.template(src, vars, true)
	if err != nil {
		return "", err
	}
	return stringify(v), nil
}

func (e *Env) walk(v any, vars map[string]any, eval bool) (any, error) {
	switch v := v.(type) {
	case string:
		return e.template(v, vars, eval)
	case map[string]any:
		// Keys in order, so that the first of several problems is always
		// the same one.
		out := make(map[string]any, len(v))
		for _, k := range slices.Sorted(maps.Keys(v)) {
			r, err := e.walk(v[k], vars, eval)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			r, err := e.walk(item, vars, eval)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	}
	return v, nil
}

func (e *Env) template(src string, vars map[string]any, eval bool) (any, error) {
	parts, err := split(src)
	if err != nil {
		return nil, err
	}
	var sb strings.Builder
	for _, p := range parts {
		if !p.expr {
			sb.WriteString(p.text)
			continue
		}
		if !eval {
			if err := e.CheckExpr(p.text); err != nil {
				return nil, fmt.Errorf("${%s}: %w", p.text, err)
			}
			continue
		}
		val, err := e.EvalExpr(p.text, vars)
		if err != nil {
			return nil, fmt.Errorf("${%s}: %w", p.text, err)
		}
		if len(parts) == 1 {
			return val, nil
		}
		sb.WriteString(stringify(val))
	}
	return sb.String(), nil
}

func (e *Env) compile(src string) (cel.Program, *cel.Type, error) {
	ast, iss := e.cel.Compile(src)
	if iss.Err() != nil {
		return nil, nil, errors.New(strings.TrimSuffix(iss.Errors()[0].Message, " (in container '')"))
	}
	// The environment holds the server's keys. Naming a variable is the
	// only way to read one, so a workflow reads only those its author wrote
	// down, and nothing from a request can choose another.
	all := 0
	for _, name := range Names(src) {
		if name == "env" {
			all++
		}
	}
	if all > len(EnvVars(src)) {
		return nil, nil, errors.New("env can only be read one variable at a time, as env.NAME")
	}
	prg, err := e.cel.Program(ast)
	if err != nil {
		return nil, nil, err
	}
	return prg, ast.OutputType(), nil
}

var parser = sync.OnceValue(func() *cel.Env {
	e, err := cel.NewEnv()
	if err != nil {
		panic(err)
	}
	return e
})

// EnvVars returns the NAME of every env.NAME that the bare expression src
// reads.
func EnvVars(src string) []string { return Fields(src, "env") }

// Fields returns the NAME of every of.NAME that the bare expression src reads.
func Fields(src, of string) []string {
	var names []string
	visit(src, func(e celast.Expr) {
		if e.Kind() != celast.SelectKind {
			return
		}
		sel := e.AsSelect()
		if op := sel.Operand(); op.Kind() == celast.IdentKind && op.AsIdent() == of {
			names = append(names, sel.FieldName())
		}
	})
	return names
}

// Names returns the names the bare expression src refers to, such as trigger,
// env, or a step's id.
func Names(src string) []string {
	var names []string
	visit(src, func(e celast.Expr) {
		// Names starting with @ are variables that macros such as exists()
		// introduce internally.
		if e.Kind() == celast.IdentKind && !strings.HasPrefix(e.AsIdent(), "@") {
			names = append(names, e.AsIdent())
		}
	})
	return names
}

func visit(src string, fn func(celast.Expr)) {
	parsed, iss := parser().Parse(src)
	if iss.Err() != nil {
		return
	}
	celast.PreOrderVisit(celast.NavigateAST(parsed.NativeRep()), celast.NewExprVisitor(fn))
}

// Placeholders returns the expression inside every ${...} in v, which may be a
// string or any nesting of maps and lists that contain strings.
func Placeholders(v any) []string {
	var exprs []string
	switch v := v.(type) {
	case string:
		parts, _ := split(v)
		for _, p := range parts {
			if p.expr {
				exprs = append(exprs, p.text)
			}
		}
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(v)) {
			exprs = append(exprs, Placeholders(v[k])...)
		}
	case []any:
		for _, item := range v {
			exprs = append(exprs, Placeholders(item)...)
		}
	}
	return exprs
}

// native converts a CEL value to the types encoding/json produces.
func native(v ref.Val) (any, error) {
	pb, err := v.ConvertToNative(reflect.TypeFor[*structpb.Value]())
	if err != nil {
		return nil, err
	}
	return pb.(*structpb.Value).AsInterface(), nil
}

func stringify(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// Single reports whether src is one ${...} placeholder and nothing else. Its
// value then keeps the type of what the placeholder yields.
func Single(src string) bool {
	parts, err := split(src)
	return err == nil && len(parts) == 1 && parts[0].expr
}

type part struct {
	text string
	expr bool
}

// split breaks src into literal text and ${...} expressions. $${ is an escaped
// literal ${.
func split(src string) ([]part, error) {
	var parts []part
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			parts = append(parts, part{text: lit.String()})
			lit.Reset()
		}
	}
	for i := 0; i < len(src); {
		switch {
		case strings.HasPrefix(src[i:], "$${"):
			lit.WriteString("${")
			i += 3
		case strings.HasPrefix(src[i:], "${"):
			end := closing(src, i+2)
			if end < 0 {
				return nil, fmt.Errorf("unclosed ${ in %q", src)
			}
			code := strings.TrimSpace(src[i+2 : end])
			if code == "" {
				return nil, fmt.Errorf("empty ${} in %q", src)
			}
			flush()
			parts = append(parts, part{text: code, expr: true})
			i = end + 1
		default:
			lit.WriteByte(src[i])
			i++
		}
	}
	flush()
	return parts, nil
}

// closing returns the index of the } that ends the placeholder whose
// expression starts at from, skipping braces nested in the expression or
// inside its string literals. It returns -1 if there is none.
func closing(src string, from int) int {
	depth, quote := 1, byte(0)
	for i := from; i < len(src); i++ {
		c := src[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '{':
			depth++
		case c == '}':
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return -1
}

// Reads returns, for each place the bare expression src reads fields of a
// named value, that name followed by the fields: ["classify", "priority",
// "value"] for classify.priority.value. A chain of fields is also listed
// by each of its beginnings.
func Reads(src string) [][]string {
	var paths [][]string
	visit(src, func(e celast.Expr) {
		var fields []string
		for e.Kind() == celast.SelectKind {
			fields = append([]string{e.AsSelect().FieldName()}, fields...)
			e = e.AsSelect().Operand()
		}
		if len(fields) > 0 && e.Kind() == celast.IdentKind {
			paths = append(paths, append([]string{e.AsIdent()}, fields...))
		}
	})
	return paths
}

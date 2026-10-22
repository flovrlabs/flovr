package expr

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func testEnv(t *testing.T) (*Env, map[string]any) {
	t.Helper()
	env, err := NewEnv([]string{"classify"})
	if err != nil {
		t.Fatal(err)
	}
	return env, map[string]any{
		"trigger":  map[string]any{"body": map[string]any{"subject": "Hi", "n": 3.0}},
		"env":      map[string]string{"URL": "http://x"},
		"classify": map[string]any{"priority": "high"},
	}
}

func TestEval(t *testing.T) {
	env, vars := testEnv(t)
	tests := []struct {
		in, want any
	}{
		{"plain", "plain"},
		{"", ""},
		{"${trigger.body.subject}", "Hi"},
		{"${trigger.body}", map[string]any{"subject": "Hi", "n": 3.0}},
		{"${env.URL}/v1?n=${trigger.body.n}", "http://x/v1?n=3"},
		{"cost: $${amount}", "cost: ${amount}"},
		{`${classify.priority + "}"}`, "high}"},
		{`${ {"a": 1}.a }`, 1.0},
		{
			map[string]any{"k": []any{"${classify.priority}", 7}},
			map[string]any{"k": []any{"high", 7}},
		},
	}
	for _, tt := range tests {
		got, err := env.Eval(tt.in, vars)
		if err != nil {
			t.Errorf("Eval(%v): %v", tt.in, err)
		} else if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Eval(%v) = %#v, want %#v", tt.in, got, tt.want)
		}
	}
}

func TestEvalErrors(t *testing.T) {
	env, vars := testEnv(t)
	tests := []struct {
		in, want string
	}{
		{"${trigger.body", "unclosed ${"},
		{"${ }", "empty ${}"},
		{"${later.summary}", "there is nothing called later here; an expression here can read trigger, classify and env"},
		{"${classify.missing}", "classify has no missing"},
		// The environment is read by naming a variable, and in no other way.
		{"${env}", "as env.NAME"},
		{`${env["URL"]}`, "as env.NAME"},
		{"${env[trigger.body.name]}", "as env.NAME"},
		{`${"URL" in env}`, "as env.NAME"},
		{"${env.all(k, true)}", "as env.NAME"},
		{`${env.URL + env["URL"]}`, "as env.NAME"},
	}
	for _, tt := range tests {
		if err := env.Check(tt.in); err == nil && strings.Contains(tt.in, "env") {
			t.Errorf("Check(%q) found nothing wrong", tt.in)
		}
		_, err := env.Eval(tt.in, vars)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("Eval(%q) error = %v, want one containing %q", tt.in, err, tt.want)
		}
	}
}

func TestCheckFindsErrorsWithoutValues(t *testing.T) {
	env, _ := testEnv(t)
	if err := env.Check(map[string]any{"a": []any{"${later.x}"}}); err == nil {
		t.Error("Check accepted a reference to an undeclared step")
	}
	// Whether a key exists is only known at run time.
	if err := env.Check("${classify.missing}"); err != nil {
		t.Errorf("Check: %v", err)
	}
}

func TestEvalExpr(t *testing.T) {
	env, vars := testEnv(t)
	got, err := env.EvalExpr(`[classify.priority, trigger.body.n + 1.0]`, vars)
	if want := []any{"high", 4.0}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("EvalExpr = %#v, %v, want %#v", got, err, want)
	}
	if err := env.CheckExpr(`classify.priority ==`); err == nil {
		t.Error("CheckExpr accepted an incomplete expression")
	}
}

func TestEnvVars(t *testing.T) {
	for src, want := range map[string][]string{
		`env.A == "x" || trigger.env.B == env.C`: {"A", "C"},
		`has(env.A) ? env.A : "none"`:            {"A", "A"},
		`classify.priority == "high"`:            nil,
		`env.A ==`:                               nil,
	} {
		if got := EnvVars(src); !reflect.DeepEqual(got, want) {
			t.Errorf("EnvVars(%s) = %v, want %v", src, got, want)
		}
	}

}

func TestNames(t *testing.T) {
	got := Names(`classify.priority == "high" && trigger.body.items.exists(i, i.total > env.LIMIT)`)
	if want := []string{"classify", "trigger", "i", "env"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Names = %v, want %v", got, want)
	}
	if got := Names(`classify.priority ==`); got != nil {
		t.Errorf("Names of an invalid expression = %v", got)
	}
}

func TestPlaceholders(t *testing.T) {
	v := map[string]any{"url": "${env.URL}/x?n=${ trigger.n }", "plain": "env.NOT_AN_EXPRESSION", "n": 3, "list": []any{"${a.b}"}}
	got := Placeholders(v)
	slices.Sort(got)
	if want := []string{"a.b", "env.URL", "trigger.n"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Placeholders = %v, want %v", got, want)
	}
}

func TestCondition(t *testing.T) {
	env, vars := testEnv(t)
	for src, want := range map[string]bool{
		`classify.priority == "high"`:                   true,
		`classify.priority == "low"`:                    false,
		`trigger.body.n > 2 && env.URL.startsWith("h")`: true,
	} {
		if err := env.CheckCondition(src); err != nil {
			t.Errorf("CheckCondition(%s): %v", src, err)
		}
		got, err := env.EvalCondition(src, vars)
		if err != nil || got != want {
			t.Errorf("EvalCondition(%s) = %v, %v, want %v", src, got, err, want)
		}
	}

	if err := env.CheckCondition(`"high"`); err == nil {
		t.Error("CheckCondition accepted a string")
	}
	if err := env.CheckCondition(`clasify.priority == "high"`); err == nil {
		t.Error("CheckCondition accepted an undeclared name")
	}
	if _, err := env.EvalCondition(`classify.priority`, vars); err == nil {
		t.Error("EvalCondition accepted a non-bool result")
	}
}

func TestReads(t *testing.T) {
	tests := map[string][]string{
		`classify.priority.value == "high"`:               {"classify.priority.value", "classify.priority"},
		`a.x > 1 && !(b.y.z in ["p", "q"])`:               {"a.x", "b.y.z", "b.y"},
		`has(summarize.summary) ? summarize.summary : ""`: {"summarize.summary", "summarize.summary"},
		`trigger.body.items.exists(i, i.price > env.MAX)`: {"trigger.body.items", "trigger.body", "i.price", "env.MAX"},
		`a.list[0].name + a.map["key"].name`:              {"a.list", "a.map"},
		`a == b`:                                          nil,
		`size(a.items) > 0`:                               {"a.items"},
		`not an expression (`:                             nil,
	}
	for src, want := range tests {
		var got []string
		for _, path := range Reads(src) {
			got = append(got, strings.Join(path, "."))
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("Reads(%q) = %v, want %v", src, got, want)
		}
	}
}

func TestWhatIsWrongWithAnExpressionIsSaidPlainly(t *testing.T) {
	env, _ := testEnv(t)
	env = env.Later("notify")
	for src, want := range map[string]string{
		`classify.priority.value ==`:        `the expression ends too soon, after "classify.priority.value =="`,
		`trigger.body.name = "a"`:           `= is not an operator; to compare, write ==`,
		`trigger.body.name == 'a`:           `the text 'a has no closing '`,
		`trigger.body.items[0`:              `a closing ] is missing at the end`,
		`(trigger.body.x == 1`:              `a closing ) is missing at the end`,
		`trigger.body.x == 1)`:              `) is not expected after "trigger.body.x == 1"`,
		`trigger..body`:                     `. is not expected after "trigger."`,
		`trigger.body.a and trigger.body.b`: `CEL writes and as &&`,
		`trigger.body.x # 2`:                `# cannot be part of an expression`,
		`upper(trigger.body.name)`:          `there is no function called upper`,
		`trigger.body.name.upper()`:         `there is no function called upper`,
		`clasify.priority`:                  `there is nothing called clasify here; an expression here can read trigger, classify and env`,
		`notify.status`:                     `notify has not run yet here; a step can read only the steps before it`,
		`size(trigger.body) + "x"`:          `+ cannot be used on these types: int, string`,
	} {
		err := env.CheckExpr(src)
		if err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", src, err, want)
		}
	}
}

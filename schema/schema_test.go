package schema

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	"github.com/flovrlabs/flovr/internal/workflow"
)

const base = "https://raw.githubusercontent.com/flovrlabs/flovr/main/schema/"

func compile(t *testing.T) (wf, comp *jsonschema.Schema) {
	t.Helper()
	c := jsonschema.NewCompiler()
	for name, data := range map[string][]byte{"workflow.json": Workflow, "component.json": Component} {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := c.AddResource(base+name, doc); err != nil {
			t.Fatal(err)
		}
	}
	wf, err := c.Compile(base + "workflow.json")
	if err != nil {
		t.Fatal(err)
	}
	comp, err = c.Compile(base + "component.json")
	if err != nil {
		t.Fatal(err)
	}
	return wf, comp
}

// asJSON reads a YAML file as the validator reads JSON.
func asJSON(t *testing.T, path string) any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	j, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(j))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// Every file in the repository that flovr validate accepts must match the
// schema too, or editors would mark good files as wrong.
func TestTheSchemasAcceptEveryValidFile(t *testing.T) {
	wf, comp := compile(t)
	checked := 0
	filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() && strings.HasPrefix(d.Name(), ".") && path != ".." {
			return filepath.SkipDir
		}
		switch d.Name() {
		case workflow.FileName:
			if _, err := workflow.Load(path); err != nil {
				return nil // a file a test means to be wrong
			}
			if err := wf.Validate(asJSON(t, path)); err != nil {
				t.Errorf("%s:\n%v", path, err)
			}
			checked++
		case workflow.ComponentFile:
			if _, err := workflow.LoadComponent(path); err != nil {
				return nil
			}
			if err := comp.Validate(asJSON(t, path)); err != nil {
				t.Errorf("%s:\n%v", path, err)
			}
			checked++
		}
		return nil
	})
	if checked < 4 {
		t.Errorf("checked %d files; the examples are missing", checked)
	}
	t.Logf("checked %d files", checked)

	// And it marks what is wrong.
	for doc, want := range map[string]string{
		`{"name": "a", "trigger": {"webhook": "/a"}, "stpes": []}`:                                                                                  "stpes",
		`{"name": "a", "trigger": {"webhook": "/a", "cron": "* * * * *"}, "steps": [{"id": "x", "use": "c"}]}`:                                      "oneOf",
		`{"name": "a", "trigger": {"webhook": "/a"}, "steps": [{"id": "x", "use": "c", "retry": {"times": 2, "on": ["sometimes"]}}]}`:               "retry/on",
		`{"name": "a", "trigger": {"webhook": "/a"}, "steps": [{"id": "x", "ai": {"model": "gpt-5", "prompt": "p", "input": "i", "output": "o"}}]}`: "model",
	} {
		v, _ := jsonschema.UnmarshalJSON(strings.NewReader(doc))
		if err := wf.Validate(v); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want it to mention %s", doc, err, want)
		}
	}
}

// The schemas name every key the Go types read, and no other.
func TestTheSchemasHaveTheKeysOfTheFiles(t *testing.T) {
	var docs map[string]map[string]any = map[string]map[string]any{}
	for name, data := range map[string][]byte{"workflow": Workflow, "component": Component} {
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		docs[name] = doc
	}
	defs := docs["workflow"]["$defs"].(map[string]any)
	keysOf := func(node map[string]any) []string {
		props, _ := node["properties"].(map[string]any)
		var keys []string
		for k := range props {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		return keys
	}
	retry := defs["retry"].(map[string]any)["oneOf"].([]any)[1].(map[string]any)
	tests := map[string]struct {
		typ  any
		node map[string]any
	}{
		"workflow":  {workflow.Workflow{}, docs["workflow"]},
		"trigger":   {workflow.Trigger{}, defs["trigger"].(map[string]any)},
		"step":      {workflow.Step{}, defs["step"].(map[string]any)},
		"ai":        {workflow.AIStep{}, defs["ai"].(map[string]any)},
		"decide":    {workflow.DecideStep{}, defs["decide"].(map[string]any)},
		"question":  {workflow.Question{}, defs["question"].(map[string]any)},
		"http":      {workflow.HTTPStep{}, defs["http"].(map[string]any)},
		"retry":     {workflow.Retry{}, retry},
		"respond":   {workflow.Respond{}, defs["respond"].(map[string]any)},
		"test":      {workflow.Test{}, defs["test"].(map[string]any)},
		"component": {workflow.Component{}, docs["component"]},
		"component test": {workflow.ComponentTest{},
			docs["component"]["properties"].(map[string]any)["tests"].(map[string]any)["items"].(map[string]any)},
	}
	for name, tt := range tests {
		var want []string
		typ := reflect.TypeOf(tt.typ)
		for i := range typ.NumField() {
			tag, _, _ := strings.Cut(typ.Field(i).Tag.Get("yaml"), ",")
			if tag != "" && tag != "-" {
				want = append(want, tag)
			}
		}
		slices.Sort(want)
		if got := keysOf(tt.node); !slices.Equal(got, want) {
			t.Errorf("%s: the schema has %v, the Go type reads %v", name, got, want)
		}
	}
}

// Command aia validates and runs AI workflows defined as YAML files.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"aia/internal/ai"
	"aia/internal/engine"
	"aia/internal/workflow"
)

func main() {
	root := &cobra.Command{
		Use:   "aia",
		Short: "Validate and run AI workflows defined as YAML files",
		Long: "Validate and run AI workflows defined as YAML files.\n\n" +
			"Model API keys come from the environment: ANTHROPIC_API_KEY, OPENAI_API_KEY or\n" +
			"OPENROUTER_API_KEY, depending on the models a workflow uses. Variables in a .env\n" +
			"file in the current directory are loaded too, unless they are already set.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(validateCmd(), runCmd(), testCmd())
	err := loadDotEnv(".env")
	if err == nil {
		err = root.Execute()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// loadDotEnv sets the variables defined as NAME=value lines in the file at
// path, if it exists, leaving alone any that already have a value.
func loadDotEnv(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for n, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || name == "" {
			// The line may hold a secret, so it is not quoted here.
			return fmt.Errorf("%s:%d: expected NAME=value", path, n+1)
		}
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		if os.Getenv(name) == "" {
			os.Setenv(name, value)
		}
	}
	return nil
}

func validateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate [workflow]",
		Short: "Check a workflow file without running it",
		Long: "Check a workflow file without running it: its structure, the prompt, schema and\n" +
			"fixture files it refers to, and every expression.\n\n" +
			"The argument is a workflow file or a directory containing workflow.yaml\n" +
			"(default: the current directory).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := workflow.Load(target(args))
			if err != nil {
				return err
			}
			fmt.Printf("ok  %s  (%s, %s)\n", w.Path, count(len(w.Steps), "step"), count(len(w.Tests), "test"))
			return nil
		},
	}
}

func runCmd() *cobra.Command {
	var fixture string
	cmd := &cobra.Command{
		Use:   "run [workflow]",
		Short: "Run a workflow once",
		Long: "Run a workflow once, for real: ai steps call the model and http steps send\n" +
			"their requests.\n\n" +
			"The argument is a workflow file or a directory containing workflow.yaml\n" +
			"(default: the current directory).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := workflow.Load(target(args))
			if err != nil {
				return err
			}
			trigger, err := loadTrigger(w, fixture)
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

			fmt.Println(w.Name)
			eng := &engine.Engine{
				Model:  ai.NewRouter(),
				HTTP:   &http.Client{Timeout: 30 * time.Second},
				Env:    environ(),
				OnStep: func(r engine.StepResult) { printStep("  ", r) },
			}
			return eng.Run(ctx, w, trigger)
		},
	}
	cmd.Flags().StringVar(&fixture, "fixture", "", "JSON file to use as the trigger event; "+
		"looked up from the current directory, then from the workflow's")
	return cmd
}

func testCmd() *cobra.Command {
	var verbose bool
	cmd := &cobra.Command{
		Use:   "test [workflow]",
		Short: "Run a workflow's tests",
		Long: "Run every entry under tests: in the workflow file. Each one runs the workflow\n" +
			"with its fixture as the trigger event and compares the result with expect.\n\n" +
			"ai steps call the model for real. http steps are not sent, so the environment\n" +
			"variables they need do not have to be set.\n\n" +
			"The argument is a workflow file or a directory containing workflow.yaml\n" +
			"(default: the current directory).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := workflow.Load(target(args))
			if err != nil {
				return err
			}
			if len(w.Tests) == 0 {
				fmt.Printf("%s has no tests\n", w.Name)
				return nil
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

			fmt.Println(w.Name)
			eng := &engine.Engine{Model: ai.NewRouter(), Env: environ()}
			failed := 0
			for _, t := range w.Tests {
				res, err := eng.Test(ctx, w, t)
				if err != nil {
					return fmt.Errorf("test %q could not run: %w", res.Name, err)
				}
				mark := "✓"
				if len(res.Failures) > 0 {
					mark = "✗"
					failed++
				}
				fmt.Printf("  %s %s  %s\n", mark, res.Name, res.Duration.Round(time.Millisecond))
				for _, f := range res.Failures {
					fmt.Printf("      %s\n", strings.ReplaceAll(f, "\n", "\n      "))
				}
				if verbose {
					for _, s := range res.Steps {
						printStep("      ", s)
					}
				}
			}

			fmt.Println()
			if failed > 0 {
				return fmt.Errorf("%d of %s failed", failed, count(len(w.Tests), "test"))
			}
			fmt.Printf("%s passed\n", count(len(w.Tests), "test"))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "show what every step did in each test")
	return cmd
}

func target(args []string) string {
	if len(args) == 0 {
		return "."
	}
	return args[0]
}

func loadTrigger(w *workflow.Workflow, fixture string) (any, error) {
	if fixture == "" {
		return map[string]any{}, nil
	}
	data, err := os.ReadFile(fixture)
	if errors.Is(err, fs.ErrNotExist) && !filepath.IsAbs(fixture) {
		data, err = os.ReadFile(w.Resolve(fixture))
	}
	if err != nil {
		return nil, fmt.Errorf("fixture: %w", err)
	}
	var trigger any
	if err := json.Unmarshal(data, &trigger); err != nil {
		return nil, fmt.Errorf("fixture %s is not valid JSON: %w", fixture, err)
	}
	return trigger, nil
}

func environ() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env
}

func printStep(indent string, r engine.StepResult) {
	mark := map[engine.Status]string{engine.OK: "✓", engine.Skipped: "-", engine.Failed: "✗"}[r.Status]
	line := fmt.Sprintf("%s%s %s  %s", indent, mark, r.ID, r.Kind)
	if r.Status == engine.Skipped {
		line += "  skipped"
	} else {
		if r.Detail != "" {
			line += "  " + r.Detail
		}
		line += "  " + r.Duration.Round(time.Millisecond).String()
	}
	fmt.Println(line)

	if r.Output != nil {
		out, _ := json.MarshalIndent(r.Output, indent+"    ", "  ")
		fmt.Printf("%s    %s\n", indent, out)
	}
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

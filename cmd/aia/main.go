// Command aia validates and runs AI workflows defined as YAML files.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"aia/internal/ai"
	"aia/internal/engine"
	"aia/internal/server"
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
	root.AddCommand(validateCmd(), runCmd(), testCmd(), serveCmd())
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
			"fixture files it refers to, the components it uses, and every expression.\n\n" +
			"The argument is a workflow file or a directory containing workflow.yaml\n" +
			"(default: the current directory). A component is checked the same way: give\n" +
			"its component.yaml or the directory containing it.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if workflow.IsComponent(target(args)) {
				c, err := workflow.LoadComponent(target(args))
				if err != nil {
					return err
				}
				fmt.Printf("ok  %s  (component, %s, %s)\n", c.Path, count(len(c.Input), "input"), count(len(c.Tests), "test"))
				return nil
			}
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
			if workflow.IsComponent(target(args)) {
				return errors.New("run takes a workflow. A component runs as part of the workflows that use it; " +
					"to try it on its own, give it tests and use: aia test " + target(args))
			}
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
			resp, err := eng.Run(ctx, w, trigger)
			if resp != nil {
				body, _ := json.MarshalIndent(resp.Body, "    ", "  ")
				fmt.Printf("  responds %d\n    %s\n", resp.Status, body)
			}
			return err
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
			"(default: the current directory). A component's own tests run the same way:\n" +
			"give its component.yaml or the directory containing it.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng := &engine.Engine{Model: ai.NewRouter(), Env: environ()}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

			// tests holds one function per test, each running it.
			var name string
			var tests []func() (engine.TestResult, error)
			if workflow.IsComponent(target(args)) {
				c, err := workflow.LoadComponent(target(args))
				if err != nil {
					return err
				}
				name = c.Name
				for _, t := range c.Tests {
					tests = append(tests, func() (engine.TestResult, error) { return eng.TestComponent(ctx, c, t) })
				}
			} else {
				w, err := workflow.Load(target(args))
				if err != nil {
					return err
				}
				name = w.Name
				for _, t := range w.Tests {
					tests = append(tests, func() (engine.TestResult, error) { return eng.Test(ctx, w, t) })
				}
			}
			if len(tests) == 0 {
				fmt.Printf("%s has no tests\n", name)
				return nil
			}

			fmt.Println(name)
			failed := 0
			for _, run := range tests {
				res, err := run()
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
				return fmt.Errorf("%d of %s failed", failed, count(len(tests), "test"))
			}
			fmt.Printf("%s passed\n", count(len(tests), "test"))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "show what every step did in each test")
	return cmd
}

func serveCmd() *cobra.Command {
	var addr, history string
	var edit bool
	cmd := &cobra.Command{
		Use:   "serve [path...]",
		Short: "Stay running and run workflows when their triggers fire",
		Long: "Stay running and run workflows when their triggers fire: a POST request to a\n" +
			"workflow's webhook path, or its cron schedule. Steps run for real, as with run.\n\n" +
			"Each path is a workflow file, or a directory that is searched for workflow.yaml\n" +
			"files (default: the current directory). Restart the server to pick up changes,\n" +
			"unless it was started with --edit.\n\n" +
			"A webhook request's JSON body, sent with Content-Type: application/json, becomes\n" +
			"trigger.body. The server answers 202 at once and runs the workflow in the\n" +
			"background; add ?wait=true to get the finished run back instead.\n\n" +
			"A workflow that has respond is an endpoint: the request waits and is answered\n" +
			"with the status and body that respond gives. If its trigger names a schema as\n" +
			"input, a body that does not match it is refused with 400 and nothing runs.\n\n" +
			"Opening the server's address in a browser shows the workflows and their recent\n" +
			"runs.\n\n" +
			"With --edit, that page can also change the workflows: it writes each change to\n" +
			"the workflow file, touching only the lines that change, so git shows exactly\n" +
			"what was edited. It can make a new workflow in a directory given as a path. The\n" +
			"server then follows the files too, taking up what an editor or git changes in\n" +
			"them. This is for working on workflows on your own machine, so it needs an\n" +
			"address that only this machine can reach.\n\n" +
			"If AIA_TOKEN is set, every webhook request must carry it as a bearer token, and\n" +
			"the browser asks for it as a password. The server only listens beyond this\n" +
			"machine when that token is set.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				args = []string{"."}
			}
			workflows, err := workflow.Discover(args...)
			if err != nil {
				return err
			}
			token := os.Getenv("AIA_TOKEN")
			if token == "" && !loopback(addr) {
				return fmt.Errorf("refusing to listen on %s without AIA_TOKEN: anyone who can reach it could run these workflows.\n"+
					"Set AIA_TOKEN, or listen on 127.0.0.1", addr)
			}
			if edit && !loopback(addr) {
				return fmt.Errorf("refusing to combine --edit with %s: the page could then change files on this machine from elsewhere.\n"+
					"Listen on 127.0.0.1 to edit", addr)
			}

			logger := log.New(os.Stdout, "", log.Ltime)
			opts := server.Options{
				Model:   ai.NewRouter(),
				HTTP:    &http.Client{Timeout: 30 * time.Second},
				Env:     environ(),
				Token:   token,
				Commit:  gitCommit(),
				History: history,
				Log:     logger.Printf,
			}
			if edit {
				// The files change while the server runs, and so does
				// their version.
				opts.Edit, opts.CommitNow = true, gitCommit
				for _, path := range args {
					if info, err := os.Stat(path); err == nil && info.IsDir() {
						opts.Roots = append(opts.Roots, filepath.Clean(path))
					}
				}
			}
			srv, err := server.New(workflows, opts)
			if err != nil {
				return err
			}
			defer srv.Close()

			listener, err := net.Listen("tcp", addr)
			if err != nil {
				return err
			}
			fmt.Printf("aia is serving on http://%s  (open it in a browser to see workflows and runs)\n", listener.Addr())
			for _, route := range srv.Routes() {
				fmt.Println("  " + route)
			}
			if token == "" {
				fmt.Println("  no AIA_TOKEN set: requests from this machine are accepted without one")
			}
			if edit {
				fmt.Println("  --edit: the page can change the workflow files, and changes to them are picked up")
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			httpSrv := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}
			stopSchedule := srv.Start()
			go func() {
				<-ctx.Done()
				logger.Printf("stopping: waiting for runs in progress")
				grace, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				httpSrv.Shutdown(grace)
			}()

			err = httpSrv.Serve(listener)
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			stopSchedule()
			srv.Wait()
			return err
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8080", "address to listen on")
	cmd.Flags().StringVar(&history, "history", filepath.Join(".aia", "runs.jsonl"),
		"file that finished runs are saved to, with their trigger payloads and step outputs; empty keeps them in memory only")
	cmd.Flags().BoolVar(&edit, "edit", false, "let the web page change the workflow files, and pick up changes made to them")
	return cmd
}

// gitCommit identifies the version of the files in the current directory: the
// short commit hash, marked if there are uncommitted changes. It is empty
// outside a git repository.
func gitCommit() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	commit := strings.TrimSpace(string(out))
	if changes, err := exec.Command("git", "status", "--porcelain").Output(); err == nil && len(changes) > 0 {
		commit += " + uncommitted changes"
	}
	return commit
}

// loopback reports whether addr can only be reached from this machine.
func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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

	// What the steps of a component made of steps did comes before what
	// the component gave back.
	for _, inner := range r.Steps {
		printStep(indent+"    ", inner)
	}
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

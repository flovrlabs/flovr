// Command flovr validates and runs AI workflows defined as YAML files.
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/flovrlabs/flovr/internal/ai"
	"github.com/flovrlabs/flovr/internal/engine"
	"github.com/flovrlabs/flovr/internal/server"
	"github.com/flovrlabs/flovr/internal/workflow"
	"github.com/flovrlabs/flovr/schema"
)

func main() {
	root := &cobra.Command{
		Use:   "flovr",
		Short: "Validate and run AI workflows defined as YAML files",
		Long: "Validate and run AI workflows defined as YAML files.\n\n" +
			"Model API keys come from the environment: ANTHROPIC_API_KEY, OPENAI_API_KEY or\n" +
			"OPENROUTER_API_KEY, depending on the models a workflow uses. Variables in a .env\n" +
			"file in the current directory are loaded too, unless they are already set.",
		Version:       currentVersion(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(validateCmd(), runCmd(), testCmd(), serveCmd(), getCmd(), schemaCmd(), versionCmd())
	err := loadDotEnv(".env")
	if err == nil {
		err = root.Execute()
	}
	if err != nil {
		if !errors.As(err, new(quiet)) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}

// version is the release this binary was built as. A release sets it with
// -ldflags "-X main.version=v0.1.0".
var version = ""

// currentVersion is the version of this binary: the release's, the module
// version that go install records, or "dev" for a build from a checkout.
func currentVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version of flovr",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("flovr " + currentVersion())
		},
	}
}

// A .env file may come with a folder from someone else, so it cannot send the
// keys of this machine elsewhere: it sets where a provider is only along with
// that provider's key, and never a proxy or which certificates to trust.
var (
	keyOf       = map[string]string{"OPENAI_BASE_URL": "OPENAI_API_KEY", "ANTHROPIC_BASE_URL": "ANTHROPIC_API_KEY"}
	notFromFile = []string{
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR",
	}
)

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
	type entry struct{ name, value string }
	var entries []entry
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
		entries = append(entries, entry{name, value})
	}
	set := map[string]bool{}
	for _, e := range entries {
		if os.Getenv(e.name) == "" && !slices.Contains(notFromFile, e.name) && keyOf[e.name] == "" {
			os.Setenv(e.name, e.value)
			set[e.name] = true
		}
	}
	for _, e := range entries {
		switch {
		case slices.Contains(notFromFile, e.name):
			fmt.Fprintf(os.Stderr, "%s: %s is not taken from a .env file; set it in the environment\n", path, e.name)
		case keyOf[e.name] != "" && os.Getenv(e.name) == "":
			if !set[keyOf[e.name]] {
				fmt.Fprintf(os.Stderr, "%s: %s is not taken from a .env file that does not also give %s\n", path, e.name, keyOf[e.name])
				continue
			}
			os.Setenv(e.name, e.value)
		}
	}
	return nil
}

// httpClient is the client http steps send their requests with. It follows a
// redirect only to the same host, and never from https to http: a request
// can carry a key in a header, which must not reach another server.
func httpClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			first := via[0].URL
			switch {
			case len(via) >= 10:
				return errors.New("stopped after 10 redirects")
			case req.URL.Host != first.Host:
				return fmt.Errorf("not following a redirect from %s to another host, %s", first.Host, req.URL.Host)
			case first.Scheme == "https" && req.URL.Scheme != "https":
				return fmt.Errorf("not following a redirect from https to %s", req.URL.Scheme)
			}
			return nil
		},
	}
}

// pageKey is the key the page and its API need: FLOVR_PAGE_KEY if it is
// set, else one kept for this user outside any project, so that a browser
// that has it keeps working across restarts. told says whether the key
// came from the environment, in which case it is not printed.
func pageKey() (key string, told bool, err error) {
	if key := os.Getenv("FLOVR_PAGE_KEY"); key != "" {
		if len(key) < minSecret {
			return "", false, fmt.Errorf("FLOVR_PAGE_KEY must be at least %d characters; make one with: openssl rand -hex 32", minSecret)
		}
		return key, true, nil
	}
	fresh := func() string { return rand.Text() + rand.Text() }
	dir, err := os.UserConfigDir()
	if err != nil {
		// Nowhere to keep it, so the key lasts as long as the server.
		return fresh(), false, nil
	}
	path := filepath.Join(dir, "flovr", "page-key")
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) >= minSecret {
		return strings.TrimSpace(string(data)), false, nil
	}
	key = fresh()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return key, false, nil
	}
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		return key, false, nil
	}
	return key, false, nil
}

// reachable is an address a browser on this machine can open for addr: the
// address itself, or 127.0.0.1 when the server listens on every address.
func reachable(addr net.Addr) string {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || !tcp.IP.IsUnspecified() {
		return addr.String()
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(tcp.Port))
}

// minSecret is the shortest token or key the server accepts.
const minSecret = 32

// clean takes control characters out of a log line, so that text from
// outside, such as a reason an http server gave, cannot rewrite the
// terminal.
func clean(line string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\t' {
			return -1
		}
		return r
	}, line)
}

func validateCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "validate [workflow]",
		Short: "Check a workflow file without running it",
		Long: "Check a workflow file without running it: its structure, the prompt, schema and\n" +
			"fixture files it refers to, the components it uses, and every expression.\n\n" +
			"The argument is a workflow file or a directory containing workflow.yaml\n" +
			"(default: the current directory). A component is checked the same way: give\n" +
			"its component.yaml or the directory containing it.\n\n" +
			"With --json it prints one JSON document, with each problem's line, for scripts\n" +
			"and coding agents; flovr schema prints the JSON Schema of the files.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			report := validateReport{Problems: []problemView{}}
			var err error
			if workflow.IsComponent(target(args)) {
				var c *workflow.Component
				report.Kind = "component"
				if c, err = workflow.LoadComponent(target(args)); err == nil {
					report.Name, report.File, report.Inputs, report.Tests = c.Name, c.Path, len(c.Input), len(c.Tests)
				}
			} else {
				var w *workflow.Workflow
				report.Kind = "workflow"
				if w, err = workflow.Load(target(args)); err == nil {
					report.Name, report.File, report.Steps, report.Tests = w.Name, w.Path, len(w.Steps), len(w.Tests)
				}
			}
			if !asJSON {
				switch {
				case err != nil:
					return err
				case report.Kind == "component":
					fmt.Printf("ok  %s  (component, %s, %s)\n", report.File, count(report.Inputs, "input"), count(report.Tests, "test"))
				default:
					fmt.Printf("ok  %s  (%s, %s)\n", report.File, count(report.Steps, "step"), count(report.Tests, "test"))
				}
				return nil
			}
			report.OK = err == nil
			var problems *workflow.Problems
			switch {
			case errors.As(err, &problems):
				report.File = problems.Path
				for _, p := range problems.Items {
					report.Problems = append(report.Problems, problemView{p.Line, p.Msg})
				}
			case err != nil:
				report.Problems = append(report.Problems, problemView{Message: err.Error()})
			}
			printJSON(report)
			if err != nil {
				return quiet{err}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the outcome as one JSON document, for scripts and coding agents")
	return cmd
}

// printJSON prints v as indented JSON, with < and > as they are.
func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// validateReport is what flovr validate --json prints.
type validateReport struct {
	OK       bool          `json:"ok"`
	Kind     string        `json:"kind"` // workflow or component
	Name     string        `json:"name,omitempty"`
	File     string        `json:"file,omitempty"`
	Steps    int           `json:"steps,omitempty"`
	Inputs   int           `json:"inputs,omitempty"`
	Tests    int           `json:"tests"`
	Problems []problemView `json:"problems"`
}

type problemView struct {
	Line    int    `json:"line,omitempty"` // 0 when the problem has no one line
	Message string `json:"message"`
}

func schemaCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "schema [workflow|component]",
		Short: "Print the JSON Schema of workflow or component files",
		Long: "Print the JSON Schema of workflow.yaml files, or with component of\n" +
			"component.yaml files. Editors and coding agents use it to know what a file can\n" +
			"hold; flovr validate checks more, such as expressions and the files a step names.\n\n" +
			"A file can name the schema for editors in its first line:\n\n" +
			"  # yaml-language-server: $schema=https://raw.githubusercontent.com/flovrlabs/flovr/main/schema/workflow.json",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"workflow", "component"},
		RunE: func(cmd *cobra.Command, args []string) error {
			switch target := "workflow"; {
			case len(args) == 1 && args[0] == "component":
				os.Stdout.Write(schema.Component)
			case len(args) == 0 || args[0] == target:
				os.Stdout.Write(schema.Workflow)
			default:
				return fmt.Errorf("flovr schema takes workflow or component, not %q", args[0])
			}
			return nil
		},
	}
}

func runCmd() *cobra.Command {
	var fixture string
	var replay bool
	cmd := &cobra.Command{
		Use:   "run [workflow]",
		Short: "Run a workflow once",
		Long: "Run a workflow once, for real: ai steps call the model and http steps send\n" +
			"their requests. With --replay, ai and decide steps get the answers that\n" +
			"flovr test --record kept instead, and no model is called.\n\n" +
			"The argument is a workflow file or a directory containing workflow.yaml\n" +
			"(default: the current directory).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if workflow.IsComponent(target(args)) {
				return errors.New("run takes a workflow. A component runs as part of the workflows that use it; " +
					"to try it on its own, give it tests and use: flovr test " + target(args))
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

			model, err := modelFor(replay, w.Path)
			if err != nil {
				return err
			}
			fmt.Println(w.Name)
			var tokens engine.Tokens
			eng := &engine.Engine{
				Model: model,
				HTTP:  httpClient(),
				Env:   environ(),
				OnStep: func(r engine.StepResult) {
					printStep("  ", r)
					tokens.Add(r.Tokens)
				},
			}
			resp, err := eng.Run(ctx, w, trigger)
			if resp != nil {
				body, _ := json.MarshalIndent(resp.Body, "    ", "  ")
				fmt.Printf("  responds %d\n    %s\n", resp.Status, body)
			}
			if tokens != (engine.Tokens{}) {
				fmt.Printf("  %s\n", tokens)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&fixture, "fixture", "", "JSON file to use as the trigger event; "+
		"looked up from the current directory, then from the workflow's")
	cmd.Flags().BoolVar(&replay, "replay", false, "answer ai and decide steps with the answers recorded by flovr test --record; no key is needed")
	return cmd
}

func testCmd() *cobra.Command {
	var verbose, record, replay bool
	var repeat int
	var minPass float64
	var maxTokens int64
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "test [workflow]",
		Short: "Run a workflow's tests",
		Long: "Run every entry under tests: in the workflow file. Each one runs the workflow\n" +
			"with its fixture as the trigger event and compares the result with expect.\n\n" +
			"ai steps call the model for real. http steps are not sent, so the environment\n" +
			"variables they need do not have to be set.\n\n" +
			"With --record, the answers the models gave are kept in " + ai.AnswersFile + " next to\n" +
			"the workflow, if every test passed. With --replay, the tests get those answers\n" +
			"and no model is called, so they run without a key and cost nothing; a test whose\n" +
			"prompt, input or schema has changed since cannot run until it is recorded again.\n\n" +
			"A model can answer differently each time, so one passing run proves little.\n" +
			"--repeat runs each test several times and says how often it passed and how\n" +
			"often each expectation was missed. A test passes only if it passed every time,\n" +
			"or as often as --min-pass asks. --max-tokens stops starting runs once the round\n" +
			"has used that many tokens.\n\n" +
			"The argument is a workflow file or a directory containing workflow.yaml\n" +
			"(default: the current directory). A component's own tests run the same way:\n" +
			"give its component.yaml or the directory containing it.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case record && replay:
				return errors.New("--record calls the models and --replay does not; give one of them")
			case repeat < 1 || repeat > 100:
				return errors.New("--repeat must be from 1 to 100")
			case repeat > 1 && replay:
				return errors.New("--repeat asks the models again each time, and replayed answers are the same every time; leave out --replay")
			case repeat > 1 && record:
				return errors.New("--record keeps the answers of one round; leave out --repeat")
			case minPass <= 0 || minPass > 1:
				return errors.New("--min-pass is the share of runs a test must pass, more than 0 and at most 1, such as 0.8")
			case maxTokens < 0:
				return errors.New("--max-tokens must be 0, for no limit, or more")
			}
			// --json prints one JSON document at the end instead.
			out := io.Writer(os.Stdout)
			if asJSON {
				out, verbose = io.Discard, false
			}
			report := testReport{Repeat: repeat, Replayed: replay, Tests: []testOutcome{}}
			eng := &engine.Engine{Env: environ()}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

			// tests holds one function per test, each running it.
			var name, file string
			var tests []func() (engine.TestResult, error)
			if workflow.IsComponent(target(args)) {
				c, err := workflow.LoadComponent(target(args))
				if err != nil {
					return err
				}
				name, file = c.Name, c.Path
				report.Kind = "component"
				for _, t := range c.Tests {
					tests = append(tests, func() (engine.TestResult, error) { return eng.TestComponent(ctx, c, t) })
				}
			} else {
				w, err := workflow.Load(target(args))
				if err != nil {
					return err
				}
				name, file = w.Name, w.Path
				report.Kind = "workflow"
				for _, t := range w.Tests {
					tests = append(tests, func() (engine.TestResult, error) { return eng.Test(ctx, w, t) })
				}
			}
			report.Name, report.File = name, file
			if len(tests) == 0 {
				fmt.Fprintf(out, "%s has no tests\n", name)
				report.OK = true
				return report.print(asJSON, nil)
			}
			answers := filepath.Join(filepath.Dir(file), ai.AnswersFile)
			var recorder *ai.Recorder
			switch {
			case replay:
				model, err := modelFor(true, file)
				if err != nil {
					return err
				}
				eng.Model = model
				fmt.Fprintln(out, name+", with the answers recorded for it")
			case record:
				recorder = ai.NewRecorder(ai.NewRouter())
				eng.Model = recorder
				fmt.Fprintln(out, name)
			case repeat > 1:
				eng.Model = ai.NewRouter()
				fmt.Fprintf(out, "%s, each test %d times\n", name, repeat)
			default:
				eng.Model = ai.NewRouter()
				fmt.Fprintln(out, name)
			}
			// Each test runs repeat times, one run after another, unless
			// the round has used its tokens first.
			var tokens engine.Tokens
			spent := func() bool { return maxTokens > 0 && tokens.In+tokens.Out >= maxTokens }
			failed, unrun, runs, passedRuns := 0, 0, 0, 0
			stopped := false // a test did not get all its runs
			for _, run := range tests {
				var results []engine.TestResult
				for len(results) < repeat && !spent() {
					res, err := run()
					for _, s := range res.Steps {
						tokens.Add(s.Tokens)
					}
					if err != nil {
						return report.print(asJSON, fmt.Errorf("test %q could not run: %w", res.Name, err))
					}
					results = append(results, res)
				}
				stopped = stopped || len(results) < repeat
				if len(results) == 0 {
					unrun++
					continue
				}
				passed := 0
				var took time.Duration
				for _, res := range results {
					if len(res.Failures) == 0 {
						passed++
					}
					took += res.Duration
				}
				runs, passedRuns = runs+len(results), passedRuns+passed
				mark := "✓"
				if len(results) < repeat || float64(passed) < minPass*float64(repeat) {
					mark = "✗"
					failed++
				}
				name := results[0].Name
				outcome := testOutcome{Name: name, Runs: len(results), Passed: passed, OK: mark == "✓", Failures: []failureCount{}}
				if repeat == 1 {
					fmt.Fprintf(out, "  %s %s  %s\n", mark, name, took.Round(time.Millisecond))
					for _, f := range results[0].Failures {
						fmt.Fprintf(out, "      %s\n", strings.ReplaceAll(f, "\n", "\n      "))
						outcome.Failures = append(outcome.Failures, failureCount{1, f})
					}
				} else {
					cut := ""
					if len(results) < repeat {
						cut = ", then the round had used its tokens"
					}
					fmt.Fprintf(out, "  %s %s  passed %d of %d%s  %s each\n", mark, name, passed, len(results), cut,
						(took / time.Duration(len(results))).Round(time.Millisecond))
					// How often each failure happened, in the order first met.
					var met []string
					times := map[string]int{}
					for _, res := range results {
						for _, f := range res.Failures {
							if times[f] == 0 {
								met = append(met, f)
							}
							times[f]++
						}
					}
					for _, f := range met {
						fmt.Fprintf(out, "      %d of %d: %s\n", times[f], len(results), strings.ReplaceAll(f, "\n", "\n      "))
						outcome.Failures = append(outcome.Failures, failureCount{times[f], f})
					}
				}
				report.Tests = append(report.Tests, outcome)
				if verbose {
					for _, res := range results {
						if repeat == 1 || len(res.Failures) > 0 {
							for _, s := range res.Steps {
								printStep("      ", s)
							}
						}
					}
				}
			}
			if unrun > 0 {
				fmt.Fprintf(out, "  %s not run: the round had used its %d tokens\n", count(unrun, "test"), maxTokens)
			}

			report.Tokens, report.NotRun = tokens, unrun
			fmt.Fprintln(out)
			used := ""
			switch {
			case replay:
				used = " · replayed, no model was called"
			case tokens != (engine.Tokens{}):
				used = " · " + tokens.String()
			}
			if repeat > 1 {
				fmt.Fprintf(out, "%d of %d runs passed%s\n", passedRuns, runs, used)
				used = ""
			}
			if stopped {
				fmt.Fprintf(out, "The round stopped starting runs at --max-tokens %d.\n", maxTokens)
			}
			if failed > 0 || unrun > 0 {
				if record {
					fmt.Fprintln(out, "The answers were not recorded, since a test failed.")
				}
				switch {
				case failed == 0:
					return report.print(asJSON, fmt.Errorf("%d of %s did not run%s", unrun, count(len(tests), "test"), used))
				case repeat == 1:
					return report.print(asJSON, fmt.Errorf("%d of %s failed%s", failed, count(len(tests), "test"), used))
				case minPass == 1:
					return report.print(asJSON, fmt.Errorf("%d of %s did not pass every time", failed, count(len(tests), "test")))
				}
				return report.print(asJSON, fmt.Errorf("%d of %s passed in fewer than %.0f%% of their runs", failed, count(len(tests), "test"), minPass*100))
			}
			switch {
			case repeat > 1 && minPass < 1:
				fmt.Fprintf(out, "%s passed in at least %.0f%% of their runs\n", count(len(tests), "test"), minPass*100)
			case repeat > 1:
				fmt.Fprintf(out, "%s passed every time\n", count(len(tests), "test"))
			default:
				fmt.Fprintf(out, "%s passed%s\n", count(len(tests), "test"), used)
			}
			if record {
				if err := recorder.Write(answers); err != nil {
					return err
				}
				if recorder.Len() == 0 {
					fmt.Fprintln(out, "No model was asked anything, so there are no answers to record.")
				} else {
					fmt.Fprintf(out, "%s recorded in %s\n", count(recorder.Len(), "answer"), answers)
				}
			}
			report.OK = true
			return report.print(asJSON, nil)
		},
	}
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "show what every step did in each test")
	cmd.Flags().BoolVar(&record, "record", false, "if every test passes, keep the models' answers in "+ai.AnswersFile+" next to the workflow")
	cmd.Flags().BoolVar(&replay, "replay", false, "answer with what --record kept instead of calling the models; no key is needed")
	cmd.Flags().IntVar(&repeat, "repeat", 1, "run each test this many times, and say how often it passed")
	cmd.Flags().Float64Var(&minPass, "min-pass", 1, "with --repeat, the share of its runs a test must pass, such as 0.8")
	cmd.Flags().Int64Var(&maxTokens, "max-tokens", 0, "stop starting runs once the round has used this many tokens; 0 is no limit")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the outcome as one JSON document, for scripts and coding agents")
	return cmd
}

// testReport is what flovr test --json prints.
type testReport struct {
	Name     string        `json:"name"`
	Kind     string        `json:"kind"` // workflow or component
	File     string        `json:"file"`
	OK       bool          `json:"ok"`
	Error    string        `json:"error,omitempty"`
	Repeat   int           `json:"repeat"`   // the runs each test was to have
	Replayed bool          `json:"replayed"` // answered from answers.jsonl, with no model called
	Tests    []testOutcome `json:"tests"`
	NotRun   int           `json:"notRun,omitempty"` // tests the token budget left out
	Tokens   engine.Tokens `json:"tokens"`
}

type testOutcome struct {
	Name     string         `json:"name"`
	OK       bool           `json:"ok"`
	Runs     int            `json:"runs"`
	Passed   int            `json:"passed"`
	Failures []failureCount `json:"failures"`
}

type failureCount struct {
	Runs    int    `json:"runs"` // in how many runs it happened
	Failure string `json:"failure"`
}

// print prints the report as JSON if asked, and returns err, the outcome as
// an error, for the exit status.
func (r *testReport) print(asJSON bool, err error) error {
	if !asJSON {
		return err
	}
	if err != nil {
		r.OK, r.Error = false, err.Error()
	}
	printJSON(r)
	if err != nil {
		return quiet{err}
	}
	return nil
}

// quiet is an error that has been reported already, as JSON; flovr exits
// with status 1 without printing it again.
type quiet struct{ error }

func serveCmd() *cobra.Command {
	var addr, history, tlsCert, tlsKey string
	var edit, replay bool
	var maxRuns int
	var market string
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
			"background; add ?wait=true to wait until it has finished.\n\n" +
			"A workflow that has respond is an endpoint: the request waits and is answered\n" +
			"with the status and body that respond gives. If its trigger names a schema as\n" +
			"input, a body that does not match it is refused with 400 and nothing runs.\n\n" +
			"Opening the address it prints, which carries the page's key, in a browser shows\n" +
			"the workflows and their recent runs. The key is kept for you outside the project,\n" +
			"so a browser that has opened it once keeps working; FLOVR_PAGE_KEY sets it.\n\n" +
			"With --edit, that page can also change the workflows: it writes each change to\n" +
			"the workflow file, touching only the lines that change, so git shows exactly\n" +
			"what was edited. It can make a new workflow in a directory given as a path. The\n" +
			"server then follows the files too, taking up what an editor or git changes in\n" +
			"them. This is for working on workflows on your own machine, so it needs an\n" +
			"address that only this machine can reach.\n\n" +
			"If FLOVR_TOKEN is set, every webhook request must carry it as a bearer token.\n" +
			"The server only listens beyond this machine when that token is set. The token\n" +
			"starts workflows and nothing else: it does not open the page, and ?wait=true then\n" +
			"answers with how the run went but not what its steps put out. Give --tls-cert and\n" +
			"--tls-key, or put the server behind a proxy that does, so the token is not sent in\n" +
			"the clear.\n\n" +
			"With --replay, ai and decide steps get the answers that flovr test --record\n" +
			"kept in " + ai.AnswersFile + " files in the paths, and no model is called: a way to try\n" +
			"the page without a key. A step asked something that was not recorded fails, and\n" +
			"http steps are still sent.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				args = []string{"."}
			}
			workflows, err := workflow.Discover(args...)
			if err != nil {
				return err
			}
			token := os.Getenv("FLOVR_TOKEN")
			if token == "" && !loopback(addr) {
				return fmt.Errorf("refusing to listen on %s without FLOVR_TOKEN: anyone who can reach it could run these workflows.\n"+
					"Set FLOVR_TOKEN, or listen on 127.0.0.1", addr)
			}
			if token != "" && len(token) < minSecret {
				return fmt.Errorf("FLOVR_TOKEN must be at least %d characters; make one with: openssl rand -hex 32", minSecret)
			}
			if (tlsCert == "") != (tlsKey == "") {
				return errors.New("give both --tls-cert and --tls-key, or neither")
			}
			key, keyFromEnv, err := pageKey()
			if err != nil {
				return err
			}
			if edit && !loopback(addr) {
				return fmt.Errorf("refusing to combine --edit with %s: the page could then change files on this machine from elsewhere.\n"+
					"Listen on 127.0.0.1 to edit", addr)
			}

			model, err := modelFor(replay, args...)
			if err != nil {
				return err
			}
			logger := log.New(os.Stdout, "", log.Ltime)
			opts := server.Options{
				Model:   model,
				Replay:  replay,
				HTTP:    httpClient(),
				Env:     environ(),
				Token:   token,
				PageKey: key,
				MaxRuns: maxRuns,
				Commit:  gitCommit(),
				History: history,
				Log: func(format string, args ...any) {
					logger.Print(clean(fmt.Sprintf(format, args...)))
				},
			}
			if edit {
				// The files change while the server runs, and so does
				// their version.
				opts.Edit, opts.CommitNow, opts.Market = true, gitCommit, market
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
			scheme := "http"
			if tlsCert != "" {
				scheme = "https"
			}
			at := reachable(listener.Addr())
			fmt.Printf("flovr is serving on %s://%s\n", scheme, at)
			if at != listener.Addr().String() {
				fmt.Printf("  listening on every address of this machine, port %d\n", listener.Addr().(*net.TCPAddr).Port)
			}
			if keyFromEnv {
				fmt.Printf("  page: %s://%s/?key= followed by FLOVR_PAGE_KEY\n", scheme, at)
			} else {
				fmt.Printf("  page: %s://%s/?key=%s\n", scheme, at, key)
			}
			for _, route := range srv.Routes() {
				fmt.Println("  " + route)
			}
			if token == "" {
				fmt.Println("  no FLOVR_TOKEN set: webhook requests from this machine are accepted without one")
			} else if tlsCert == "" {
				fmt.Println("  no --tls-cert: the token travels in the clear unless a proxy in front adds TLS")
			}
			if edit {
				fmt.Println("  --edit: the page can change the workflow files, and changes to them are picked up")
			}
			if replay {
				fmt.Printf("  --replay: ai and decide steps get the %s recorded in %s; no model is called\n",
					count(model.(*ai.Replay).Len(), "answer"), ai.AnswersFile)
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			// Requests that wait for a run are answered within the run's
			// five minutes, so only reading and idling are bounded.
			httpSrv := &http.Server{
				Handler: srv, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: time.Minute,
				IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 64 << 10,
			}
			stopSchedule := srv.Start()
			go func() {
				<-ctx.Done()
				logger.Printf("stopping: waiting for runs in progress")
				grace, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				httpSrv.Shutdown(grace)
			}()

			if tlsCert != "" {
				err = httpSrv.ServeTLS(listener, tlsCert, tlsKey)
			} else {
				err = httpSrv.Serve(listener)
			}
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			stopSchedule()
			srv.Wait()
			return err
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8080", "address to listen on")
	cmd.Flags().StringVar(&history, "history", filepath.Join(".flovr", "runs.jsonl"),
		"file that finished runs are saved to, with their trigger payloads and step outputs; empty keeps them in memory only")
	cmd.Flags().BoolVar(&edit, "edit", false, "let the web page change the workflow files, and pick up changes made to them")
	cmd.Flags().BoolVar(&replay, "replay", false, "answer ai and decide steps with the answers recorded by flovr test --record; no key is needed")
	cmd.Flags().IntVar(&maxRuns, "max-runs", 16, "how many runs webhooks may have going at once; more are answered 429")
	cmd.Flags().StringVar(&market, "market", defaultMarket, "with --edit, the list of components in git that the page offers: an https address or a file; empty offers none")
	cmd.Flags().StringVar(&tlsCert, "tls-cert", "", "certificate file, to serve https")
	cmd.Flags().StringVar(&tlsKey, "tls-key", "", "key file of the certificate")
	return cmd
}

// defaultMarket is the list of components the page offers unless told otherwise.
const defaultMarket = "https://raw.githubusercontent.com/flovrlabs/flovr/main/market/index.json"

// gitCommit identifies the version of the files in the current directory: the
// short commit hash, marked if there are uncommitted changes. It is empty
// outside a git repository.
func gitCommit() string {
	// The repository's own settings cannot make git run other programs.
	git := func(args ...string) *exec.Cmd {
		return exec.Command("git", append([]string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=" + os.DevNull}, args...)...)
	}
	out, err := git("rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	commit := strings.TrimSpace(string(out))
	if changes, err := git("status", "--porcelain").Output(); err == nil && len(changes) > 0 {
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

// modelFor is what answers ai and decide steps: the models, or with replay the
// answers recorded in the folders at paths.
func modelFor(replay bool, paths ...string) (engine.Model, error) {
	if !replay {
		return ai.NewRouter(), nil
	}
	answers, err := ai.LoadAnswers(paths...)
	if err != nil {
		return nil, err
	}
	return answers, nil
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
	// An answer the step could not use is shown as the model wrote it.
	var invalid *engine.InvalidAnswerError
	if errors.As(r.Err, &invalid) {
		answer := invalid.Raw
		if len(answer) > 2000 {
			answer = answer[:2000] + "…"
		}
		if strings.TrimSpace(answer) == "" {
			answer = "(nothing)"
		}
		fmt.Printf("%s    the model answered:\n%s      %s\n", indent, indent, strings.ReplaceAll(answer, "\n", "\n"+indent+"      "))
	}

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

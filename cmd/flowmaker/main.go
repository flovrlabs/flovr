// Command flowmaker validates and runs AI workflows defined as YAML files.
package main

import (
	"context"
	"crypto/rand"
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
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/simonyosb/flowmaker/internal/ai"
	"github.com/simonyosb/flowmaker/internal/engine"
	"github.com/simonyosb/flowmaker/internal/server"
	"github.com/simonyosb/flowmaker/internal/workflow"
)

func main() {
	root := &cobra.Command{
		Use:   "flowmaker",
		Short: "Validate and run AI workflows defined as YAML files",
		Long: "Validate and run AI workflows defined as YAML files.\n\n" +
			"Model API keys come from the environment: ANTHROPIC_API_KEY, OPENAI_API_KEY or\n" +
			"OPENROUTER_API_KEY, depending on the models a workflow uses. Variables in a .env\n" +
			"file in the current directory are loaded too, unless they are already set.",
		Version:       currentVersion(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(validateCmd(), runCmd(), testCmd(), serveCmd(), getCmd(), versionCmd())
	err := loadDotEnv(".env")
	if err == nil {
		err = root.Execute()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
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
		Short: "Print the version of flowmaker",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("flowmaker " + currentVersion())
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

// pageKey is the key the page and its API need: FLOWMAKER_PAGE_KEY if it is
// set, else one kept for this user outside any project, so that a browser
// that has it keeps working across restarts. told says whether the key
// came from the environment, in which case it is not printed.
func pageKey() (key string, told bool, err error) {
	if key := os.Getenv("FLOWMAKER_PAGE_KEY"); key != "" {
		if len(key) < minSecret {
			return "", false, fmt.Errorf("FLOWMAKER_PAGE_KEY must be at least %d characters; make one with: openssl rand -hex 32", minSecret)
		}
		return key, true, nil
	}
	fresh := func() string { return rand.Text() + rand.Text() }
	dir, err := os.UserConfigDir()
	if err != nil {
		// Nowhere to keep it, so the key lasts as long as the server.
		return fresh(), false, nil
	}
	path := filepath.Join(dir, "flowmaker", "page-key")
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
					"to try it on its own, give it tests and use: flowmaker test " + target(args))
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
				HTTP:   httpClient(),
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
	var addr, history, tlsCert, tlsKey string
	var edit bool
	var maxRuns int
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
			"so a browser that has opened it once keeps working; FLOWMAKER_PAGE_KEY sets it.\n\n" +
			"With --edit, that page can also change the workflows: it writes each change to\n" +
			"the workflow file, touching only the lines that change, so git shows exactly\n" +
			"what was edited. It can make a new workflow in a directory given as a path. The\n" +
			"server then follows the files too, taking up what an editor or git changes in\n" +
			"them. This is for working on workflows on your own machine, so it needs an\n" +
			"address that only this machine can reach.\n\n" +
			"If FLOWMAKER_TOKEN is set, every webhook request must carry it as a bearer token.\n" +
			"The server only listens beyond this machine when that token is set. The token\n" +
			"starts workflows and nothing else: it does not open the page, and ?wait=true then\n" +
			"answers with how the run went but not what its steps put out. Give --tls-cert and\n" +
			"--tls-key, or put the server behind a proxy that does, so the token is not sent in\n" +
			"the clear.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				args = []string{"."}
			}
			workflows, err := workflow.Discover(args...)
			if err != nil {
				return err
			}
			token := os.Getenv("FLOWMAKER_TOKEN")
			if token == "" && !loopback(addr) {
				return fmt.Errorf("refusing to listen on %s without FLOWMAKER_TOKEN: anyone who can reach it could run these workflows.\n"+
					"Set FLOWMAKER_TOKEN, or listen on 127.0.0.1", addr)
			}
			if token != "" && len(token) < minSecret {
				return fmt.Errorf("FLOWMAKER_TOKEN must be at least %d characters; make one with: openssl rand -hex 32", minSecret)
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

			logger := log.New(os.Stdout, "", log.Ltime)
			opts := server.Options{
				Model:   ai.NewRouter(),
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
			scheme := "http"
			if tlsCert != "" {
				scheme = "https"
			}
			at := reachable(listener.Addr())
			fmt.Printf("flowmaker is serving on %s://%s\n", scheme, at)
			if at != listener.Addr().String() {
				fmt.Printf("  listening on every address of this machine, port %d\n", listener.Addr().(*net.TCPAddr).Port)
			}
			if keyFromEnv {
				fmt.Printf("  page: %s://%s/?key= followed by FLOWMAKER_PAGE_KEY\n", scheme, at)
			} else {
				fmt.Printf("  page: %s://%s/?key=%s\n", scheme, at, key)
			}
			for _, route := range srv.Routes() {
				fmt.Println("  " + route)
			}
			if token == "" {
				fmt.Println("  no FLOWMAKER_TOKEN set: webhook requests from this machine are accepted without one")
			} else if tlsCert == "" {
				fmt.Println("  no --tls-cert: the token travels in the clear unless a proxy in front adds TLS")
			}
			if edit {
				fmt.Println("  --edit: the page can change the workflow files, and changes to them are picked up")
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
	cmd.Flags().StringVar(&history, "history", filepath.Join(".flowmaker", "runs.jsonl"),
		"file that finished runs are saved to, with their trigger payloads and step outputs; empty keeps them in memory only")
	cmd.Flags().BoolVar(&edit, "edit", false, "let the web page change the workflow files, and pick up changes made to them")
	cmd.Flags().IntVar(&maxRuns, "max-runs", 16, "how many runs webhooks may have going at once; more are answered 429")
	cmd.Flags().StringVar(&tlsCert, "tls-cert", "", "certificate file, to serve https")
	cmd.Flags().StringVar(&tlsKey, "tls-key", "", "key file of the certificate")
	return cmd
}

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

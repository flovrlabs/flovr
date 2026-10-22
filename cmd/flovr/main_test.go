package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := `# keys for local runs
FLOVR_TEST_PLAIN=abc=def

export FLOVR_TEST_EXPORTED = "quoted value"
FLOVR_TEST_SINGLE='single'
FLOVR_TEST_ALREADY_SET=from file
FLOVR_TEST_EMPTY=
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"FLOVR_TEST_PLAIN", "FLOVR_TEST_EXPORTED", "FLOVR_TEST_SINGLE", "FLOVR_TEST_EMPTY"} {
		t.Setenv(name, "")
	}
	t.Setenv("FLOVR_TEST_ALREADY_SET", "from environment")

	if err := loadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"FLOVR_TEST_PLAIN":       "abc=def",
		"FLOVR_TEST_EXPORTED":    "quoted value",
		"FLOVR_TEST_SINGLE":      "single",
		"FLOVR_TEST_ALREADY_SET": "from environment",
		"FLOVR_TEST_EMPTY":       "",
	} {
		if got := os.Getenv(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestLoadDotEnvProblems(t *testing.T) {
	if err := loadDotEnv(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Errorf("a missing file is not an error, got %v", err)
	}

	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("OK=1\nsk-secret-without-a-name\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OK", "")
	err := loadDotEnv(path)
	if want := path + ":2: expected NAME=value"; err == nil || err.Error() != want {
		t.Errorf("error = %v, want %s", err, want)
	}
}

func TestDotEnvDoesNotSendKeysElsewhere(t *testing.T) {
	dir := t.TempDir()
	write := func(content string) string {
		path := filepath.Join(dir, ".env")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, name := range []string{"OPENAI_BASE_URL", "OPENAI_API_KEY", "ANTHROPIC_BASE_URL", "HTTPS_PROXY", "SSL_CERT_FILE"} {
		t.Setenv(name, "")
	}
	// The key is this machine's, so the file may not say where it goes.
	t.Setenv("ANTHROPIC_API_KEY", "from the environment")
	if err := loadDotEnv(write("ANTHROPIC_BASE_URL=https://elsewhere.example/\nHTTPS_PROXY=http://proxy.example\nSSL_CERT_FILE=/tmp/ca.pem\n")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ANTHROPIC_BASE_URL", "HTTPS_PROXY", "SSL_CERT_FILE"} {
		if got := os.Getenv(name); got != "" {
			t.Errorf("%s = %q, want it left unset", name, got)
		}
	}
	// A file that gives the key as well may say where it goes.
	if err := loadDotEnv(write("OPENAI_BASE_URL=http://127.0.0.1:11434/v1/\nOPENAI_API_KEY=local\n")); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("OPENAI_BASE_URL"); got != "http://127.0.0.1:11434/v1/" {
		t.Errorf("OPENAI_BASE_URL = %q", got)
	}
}

func TestHTTPStepsFollowRedirectsOnlyToTheSameHost(t *testing.T) {
	var got []string
	other := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		got = append(got, "other "+r.Header.Get("X-Api-Key"))
	}))
	defer other.Close()
	var same *httptest.Server
	same = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/away":
			http.Redirect(rw, r, other.URL+"/", http.StatusFound)
		case "/here":
			http.Redirect(rw, r, same.URL+"/end", http.StatusFound)
		default:
			got = append(got, "same "+r.Header.Get("X-Api-Key"))
		}
	}))
	defer same.Close()

	client := httpClient()
	for path, wantErr := range map[string]bool{"/here": false, "/away": true} {
		req, _ := http.NewRequest(http.MethodGet, same.URL+path, nil)
		req.Header.Set("X-Api-Key", "secret")
		resp, err := client.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		if (err != nil) != wantErr {
			t.Errorf("GET %s: error = %v", path, err)
		}
	}
	if strings.Join(got, ";") != "same secret" {
		t.Errorf("requests that arrived: %q", got)
	}
}

func TestLogLinesLoseControlCharacters(t *testing.T) {
	if got := clean("step failed: 500 \x1b[2K\rall good\tok"); got != "step failed: 500 [2Kall good\tok" {
		t.Errorf("clean = %q", got)
	}
}

func TestPageKeyIsKeptForTheUser(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
	t.Setenv("FLOVR_PAGE_KEY", "")
	first, fromEnv, err := pageKey()
	if err != nil || fromEnv || len(first) < minSecret {
		t.Fatalf("key = %q, from the environment %v, error %v", first, fromEnv, err)
	}
	if again, _, _ := pageKey(); again != first {
		t.Errorf("the key changed between starts: %q, then %q", first, again)
	}

	t.Setenv("FLOVR_PAGE_KEY", "short")
	if _, _, err := pageKey(); err == nil {
		t.Error("a short FLOVR_PAGE_KEY was accepted")
	}
	t.Setenv("FLOVR_PAGE_KEY", strings.Repeat("k", minSecret))
	if key, fromEnv, err := pageKey(); err != nil || !fromEnv || key != strings.Repeat("k", minSecret) {
		t.Errorf("key = %q, from the environment %v, error %v", key, fromEnv, err)
	}
}

func TestAddressPrintedIsOneABrowserCanOpen(t *testing.T) {
	for addr, want := range map[string]string{
		"0.0.0.0:8080":   "127.0.0.1:8080",
		"[::]:8080":      "127.0.0.1:8080",
		"127.0.0.1:8080": "127.0.0.1:8080",
		"10.0.0.5:9000":  "10.0.0.5:9000",
	} {
		tcp, err := net.ResolveTCPAddr("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		if got := reachable(tcp); got != want {
			t.Errorf("reachable(%s) = %s, want %s", addr, got, want)
		}
	}
}

// The answers recorded for the examples must be those their files ask for now,
// so that the examples can be tried without a key.
func TestExamplesPassWithRecordedAnswers(t *testing.T) {
	examples, _ := filepath.Glob(filepath.Join("..", "..", "examples", "*", "workflow.yaml"))
	components, _ := filepath.Glob(filepath.Join("..", "..", "examples", "*", "components", "*", "component.yaml"))
	if len(examples) == 0 || len(components) == 0 {
		t.Fatal("no examples found")
	}
	for _, target := range append(examples, components...) {
		cmd := testCmd()
		cmd.SilenceUsage = true
		cmd.SetArgs([]string{"--replay", target})
		if err := cmd.Execute(); err != nil {
			t.Errorf("%s: %v", target, err)
		}
	}
}

// stdout returns what f printed.
func stdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	defer func() { os.Stdout = saved }()
	f()
	w.Close()
	os.Stdout = saved
	return <-done
}

// TestRepeatSaysHowOftenATestPassed runs a test four times against a model
// that answers "down" and "up" in turn.
func TestRepeatSaysHowOftenATestPassed(t *testing.T) {
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		summary := "down"
		if calls.Add(1)%2 == 0 {
			summary = "up"
		}
		content, _ := json.Marshal(map[string]string{"summary": summary})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "x", "object": "chat.completion", "model": "m",
			"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(content)}}},
			"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": 4},
		})
	}))
	t.Cleanup(model.Close)
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", model.URL+"/v1/")

	dir := t.TempDir()
	files := map[string]string{
		"workflow.yaml": "name: rep\ntrigger:\n  webhook: /r\nsteps:\n  - id: summarize\n" +
			"    ai: {model: openai/m, prompt: p.md, input: \"${trigger.body}\", output: s.json}\n" +
			"tests:\n  - name: it says down\n    fixture: f.json\n    expect:\n      summarize.summary: down\n",
		"p.md":   "Summarize.",
		"s.json": `{"type": "object", "properties": {"summary": {"type": "string"}}, "required": ["summary"]}`,
		"f.json": `{"body": "x"}`,
	}
	for name, content := range files {
		os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}

	run := func(args ...string) (string, error) {
		cmd := testCmd()
		cmd.SilenceUsage = true
		cmd.SetArgs(append(args, dir))
		var err error
		out := stdout(t, func() { err = cmd.Execute() })
		return out, err
	}
	out, err := run("--repeat", "4")
	if err == nil || !strings.Contains(err.Error(), "did not pass every time") {
		t.Errorf("err = %v", err)
	}
	for _, want := range []string{"passed 2 of 4", `2 of 4: summarize.summary: expected "down", got "up"`, "2 of 4 runs passed · 12 tokens in, 16 out"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if out, err := run("--repeat", "4", "--min-pass", "0.5"); err != nil || !strings.Contains(out, "at least 50% of their runs") {
		t.Errorf("with --min-pass 0.5: err = %v, output:\n%s", err, out)
	}
	if _, err := run("--repeat", "2", "--replay"); err == nil {
		t.Error("--repeat with --replay was taken")
	}
}

func TestValidateSaysWhatIsWrongAsJSON(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "workflow.yaml"), []byte("flovr: 2\nname: bad\ntrigger:\n  webhook: /x\nsteps:\n  - id: a\n    retry: {times: 20}\n    http: {method: POST, url: \"http://x\"}\n"), 0o644)
	cmd := validateCmd()
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"--json", dir})
	var err error
	out := stdout(t, func() { err = cmd.Execute() })
	if !errors.As(err, new(quiet)) {
		t.Errorf("err = %v, want one already reported", err)
	}
	var report validateReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("%v in:\n%s", err, out)
	}
	if report.OK || len(report.Problems) != 2 || report.Problems[0].Line != 1 || report.Problems[1].Line != 7 ||
		!strings.Contains(report.Problems[1].Message, "retry: times must be from 1 to 10") {
		t.Errorf("report = %+v", report)
	}
}

func TestARunOfAWebhookSaysWhichFixturesToGive(t *testing.T) {
	target := filepath.Join("..", "..", "examples", "triage-ticket")
	cmd := runCmd()
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{target})
	var err error
	stdout(t, func() { err = cmd.Execute() })
	if err == nil || !strings.Contains(err.Error(), "starts on a request to /tickets, so a run needs one") ||
		!strings.Contains(err.Error(), "\n  flovr run "+target+" --fixture fixtures/outage.json") {
		t.Errorf("err = %v", err)
	}
}

func TestWithoutAKeyTheRecordedAnswersAreOffered(t *testing.T) {
	// No request is sent without a key.
	t.Setenv("OPENROUTER_API_KEY", "")
	target := filepath.Join("..", "..", "examples", "triage-ticket")
	cmd := testCmd()
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{target})
	var err error
	stdout(t, func() { err = cmd.Execute() })
	if err == nil || !strings.Contains(err.Error(), "set OPENROUTER_API_KEY") ||
		!strings.Contains(err.Error(), "\n  flovr test "+target+" --replay") {
		t.Errorf("err = %v", err)
	}
	if got := shellQuote("fixtures/a b.json"); got != "'fixtures/a b.json'" {
		t.Errorf("shellQuote = %s", got)
	}
}

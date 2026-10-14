package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := `# keys for local runs
FLOWMAKER_TEST_PLAIN=abc=def

export FLOWMAKER_TEST_EXPORTED = "quoted value"
FLOWMAKER_TEST_SINGLE='single'
FLOWMAKER_TEST_ALREADY_SET=from file
FLOWMAKER_TEST_EMPTY=
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"FLOWMAKER_TEST_PLAIN", "FLOWMAKER_TEST_EXPORTED", "FLOWMAKER_TEST_SINGLE", "FLOWMAKER_TEST_EMPTY"} {
		t.Setenv(name, "")
	}
	t.Setenv("FLOWMAKER_TEST_ALREADY_SET", "from environment")

	if err := loadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"FLOWMAKER_TEST_PLAIN":       "abc=def",
		"FLOWMAKER_TEST_EXPORTED":    "quoted value",
		"FLOWMAKER_TEST_SINGLE":      "single",
		"FLOWMAKER_TEST_ALREADY_SET": "from environment",
		"FLOWMAKER_TEST_EMPTY":       "",
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
	t.Setenv("FLOWMAKER_PAGE_KEY", "")
	first, fromEnv, err := pageKey()
	if err != nil || fromEnv || len(first) < minSecret {
		t.Fatalf("key = %q, from the environment %v, error %v", first, fromEnv, err)
	}
	if again, _, _ := pageKey(); again != first {
		t.Errorf("the key changed between starts: %q, then %q", first, again)
	}

	t.Setenv("FLOWMAKER_PAGE_KEY", "short")
	if _, _, err := pageKey(); err == nil {
		t.Error("a short FLOWMAKER_PAGE_KEY was accepted")
	}
	t.Setenv("FLOWMAKER_PAGE_KEY", strings.Repeat("k", minSecret))
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

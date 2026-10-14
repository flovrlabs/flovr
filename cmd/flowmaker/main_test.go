package main

import (
	"os"
	"path/filepath"
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

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeDotEnvTree creates repoRoot/.env with the given content and
// repoRoot/a/b as a nested subdirectory, returning both paths.
func writeDotEnvTree(t *testing.T, envContent string) (repoRoot, nestedDir string) {
	t.Helper()
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte(envContent), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	return root, nested
}

// clearEnv ensures a variable is unset both before and after the test, so
// loadDotEnv's os.Setenv calls (which, unlike t.Setenv, are not
// auto-restored) never leak into other tests in this package.
func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		os.Unsetenv(k)
		t.Cleanup(func(k string) func() { return func() { os.Unsetenv(k) } }(k))
	}
}

func TestLoad_ReadsDotEnvFromCurrentDirectory(t *testing.T) {
	clearEnv(t, "PORT")
	root, _ := writeDotEnvTree(t, "PORT=8087\n")
	t.Chdir(root)

	cfg := Load()

	if cfg.Port != "8087" {
		t.Fatalf("Port = %q, want 8087 (from .env in cwd)", cfg.Port)
	}
}

func TestLoad_ReadsDotEnvFromAncestorDirectory(t *testing.T) {
	// Reproduces `cd cmd/discovery && go run .` — the process's cwd is a
	// subdirectory, but .env lives at the repo root.
	clearEnv(t, "PORT")
	root, nested := writeDotEnvTree(t, "PORT=9099\n")
	_ = root
	t.Chdir(nested)

	cfg := Load()

	if cfg.Port != "9099" {
		t.Fatalf("Port = %q, want 9099 (from .env two levels up)", cfg.Port)
	}
}

func TestLoad_RealEnvVarTakesPrecedenceOverDotEnv(t *testing.T) {
	root, _ := writeDotEnvTree(t, "PORT=8087\n")
	t.Chdir(root)
	t.Setenv("PORT", "7777") // a real, explicitly-set env var

	cfg := Load()

	if cfg.Port != "7777" {
		t.Fatalf("Port = %q, want 7777 (explicit env must win over .env)", cfg.Port)
	}
}

func TestLoad_IgnoresBlankLinesAndComments(t *testing.T) {
	clearEnv(t, "PORT", "HOST_PORT")
	root, _ := writeDotEnvTree(t, "# a comment\n\nPORT=8087\n  \n# HOST_PORT=9999 (commented out)\n")
	t.Chdir(root)

	cfg := Load()

	if cfg.Port != "8087" {
		t.Fatalf("Port = %q, want 8087", cfg.Port)
	}
	if os.Getenv("HOST_PORT") != "" {
		t.Fatalf("HOST_PORT should not be set from a commented-out line, got %q", os.Getenv("HOST_PORT"))
	}
}

func TestLoad_NoDotEnvAnywhere_UsesDefaults(t *testing.T) {
	clearEnv(t, "PORT")
	// A fresh temp dir with no .env in it or any ancestor up to the bound
	// loadDotEnv searches — Load() must still return the built-in default
	// rather than erroring.
	t.Chdir(t.TempDir())

	cfg := Load()

	if cfg.Port != "8080" {
		t.Fatalf("Port = %q, want default 8080 when no .env exists", cfg.Port)
	}
}

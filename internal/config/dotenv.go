package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// maxDotEnvSearchDepth bounds how far up the directory tree loadDotEnv
// will walk looking for a .env file, so a missing .env costs at most this
// many stat calls rather than searching indefinitely toward "/".
const maxDotEnvSearchDepth = 8

// loadDotEnv finds and applies a ".env" file before Load() reads
// environment variables, so the service is configured the same way
// whether it's started via `docker compose` (which sources .env itself)
// or run directly (`go run ./cmd/discovery`, or `go run .` from inside
// cmd/discovery/ — hence walking upward, not just checking the cwd).
//
// Real environment variables always win: a variable already set (by the
// shell, systemd, docker-compose's `environment:` block, etc.) is never
// overwritten by .env — this only fills in what's not already set,
// standard dotenv semantics.
func loadDotEnv() {
	dir, err := os.Getwd()
	if err != nil {
		return
	}

	for range maxDotEnvSearchDepth {
		path := filepath.Join(dir, ".env")
		if data, err := os.ReadFile(path); err == nil {
			applyDotEnv(data)
			return
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return // reached filesystem root without finding one
		}
		dir = parent
	}
}

func applyDotEnv(data []byte) {
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)

		if _, alreadySet := os.LookupEnv(key); alreadySet {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			slog.Warn("failed to apply .env variable", "key", key, "error", err)
		}
	}
}

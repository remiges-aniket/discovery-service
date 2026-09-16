package logging

import (
	"testing"

	"github.com/remiges-tech/logharbour/logharbour"
)

func TestParsePriority(t *testing.T) {
	cases := []struct {
		level string
		want  logharbour.LogPriority
	}{
		{"debug2", logharbour.Debug2},
		{"DEBUG2", logharbour.Debug2},
		{"debug1", logharbour.Debug1},
		{"debug0", logharbour.Debug0},
		{"debug", logharbour.Debug0},
		{" Debug ", logharbour.Debug0},
		{"info", logharbour.Info},
		{"", logharbour.Info},
		{"warn", logharbour.Warn},
		{"warning", logharbour.Warn},
		{"err", logharbour.Err},
		{"error", logharbour.Err},
		{"crit", logharbour.Crit},
		{"critical", logharbour.Crit},
		{"sec", logharbour.Sec},
		{"security", logharbour.Sec},
		{"something-unrecognized", logharbour.Info},
	}
	for _, tc := range cases {
		t.Run(tc.level, func(t *testing.T) {
			if got := ParsePriority(tc.level); got != tc.want {
				t.Errorf("ParsePriority(%q) = %v, want %v", tc.level, got, tc.want)
			}
		})
	}
}

func TestIsDebugLevel(t *testing.T) {
	debugLevels := []logharbour.LogPriority{logharbour.Debug2, logharbour.Debug1, logharbour.Debug0}
	for _, p := range debugLevels {
		if !IsDebugLevel(p) {
			t.Errorf("IsDebugLevel(%v) = false, want true", p)
		}
	}

	nonDebugLevels := []logharbour.LogPriority{logharbour.Info, logharbour.Warn, logharbour.Err, logharbour.Crit, logharbour.Sec}
	for _, p := range nonDebugLevels {
		if IsDebugLevel(p) {
			t.Errorf("IsDebugLevel(%v) = true, want false", p)
		}
	}
}

func TestNew_ReturnsAWorkingLogger(t *testing.T) {
	// Smoke test: New() must not panic and must return a usable *Logger
	// regardless of priority/debug settings — the actual output routing
	// (stdout+stderr fallback) is exercised by construction succeeding.
	logger := New("test-app", logharbour.Info, false)
	if logger == nil {
		t.Fatal("New() returned nil")
	}
	logger.LogActivity("smoke test", map[string]any{"ok": true})
}

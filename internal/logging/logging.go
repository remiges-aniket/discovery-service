// Package logging wraps github.com/remiges-tech/logharbour to give the
// rest of the service one place to build and configure a Logger from
// runtime config (LOG_LEVEL in .env — see CONTEXT.md D10), rather than
// scattering priority-string parsing and writer setup at every call site.
package logging

import (
	"os"
	"strings"

	"github.com/remiges-tech/logharbour/logharbour"
)

// New builds a Logger that writes structured JSON log lines to stdout
// (falling back to stderr if stdout ever fails to write), gated at
// minPriority, with logharbour's separate "debug mode" flag set from
// debugMode (LogDebug entries are dropped unless BOTH the priority check
// passes AND debug mode is on — see IsDebugLevel).
func New(appName string, minPriority logharbour.LogPriority, debugMode bool) *logharbour.Logger {
	ctx := logharbour.NewLoggerContext(minPriority)
	ctx.SetDebugMode(debugMode)

	fallbackWriter := logharbour.NewFallbackWriter(os.Stdout, os.Stderr)
	return logharbour.NewLoggerWithFallback(ctx, appName, fallbackWriter)
}

// ParsePriority maps a LOG_LEVEL value from .env (case-insensitive,
// surrounding whitespace ignored) to a logharbour.LogPriority. An empty
// or unrecognized value defaults to Info — the same "runs with zero
// config" contract as the rest of internal/config.
func ParsePriority(level string) logharbour.LogPriority {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug2":
		return logharbour.Debug2
	case "debug1":
		return logharbour.Debug1
	case "debug0", "debug":
		return logharbour.Debug0
	case "warn", "warning":
		return logharbour.Warn
	case "err", "error":
		return logharbour.Err
	case "crit", "critical":
		return logharbour.Crit
	case "sec", "security":
		return logharbour.Sec
	default:
		return logharbour.Info
	}
}

// IsDebugLevel reports whether p is one of Debug0/Debug1/Debug2.
// logharbour.Logger.LogDebug only emits an entry when the logger's
// LoggerContext has debug mode explicitly enabled — priority alone isn't
// enough — so callers use this to decide whether to also call
// LoggerContext.SetDebugMode(true) when LOG_LEVEL selects a debug tier.
func IsDebugLevel(p logharbour.LogPriority) bool {
	return p == logharbour.Debug0 || p == logharbour.Debug1 || p == logharbour.Debug2
}

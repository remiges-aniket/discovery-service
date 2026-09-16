package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/remiges-tech/logharbour/logharbour"
)

// newCapturingLogger returns a Logger writing to buf (as its primary
// writer, with io.Discard as fallback) so tests can assert on emitted
// log lines instead of just trusting nothing panicked.
func newCapturingLogger(buf *bytes.Buffer) *logharbour.Logger {
	ctx := logharbour.NewLoggerContext(logharbour.Info)
	return logharbour.NewLoggerWithFallback(ctx, "handlers-test", logharbour.NewFallbackWriter(buf, io.Discard))
}

func TestLoggingMiddleware_LogsEveryHit(t *testing.T) {
	var buf bytes.Buffer
	logger := newCapturingLogger(&buf)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	wrapped := LoggingMiddleware(logger)(inner)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want %d (middleware must not alter the response)", rec.Code, http.StatusTeapot)
	}

	logged := buf.String()
	if logged == "" {
		t.Fatal("expected a log line for the request, got none — every hit must be visible")
	}
	if !strings.Contains(logged, "/healthz") {
		t.Errorf("log line missing request path: %s", logged)
	}
	if !strings.Contains(logged, "418") { // http.StatusTeapot
		t.Errorf("log line missing response status: %s", logged)
	}
}

func TestLoggingMiddleware_DefaultsStatusTo200WhenHandlerNeverCallsWriteHeader(t *testing.T) {
	var buf bytes.Buffer
	logger := newCapturingLogger(&buf)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok")) // implicit 200, no explicit WriteHeader call
	})
	wrapped := LoggingMiddleware(logger)(inner)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	var entry map[string]any
	line := firstNonEmptyLine(buf.String())
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		t.Fatalf("log line is not valid JSON: %v (line=%q)", err, line)
	}
	data, _ := entry["data"].(map[string]any)
	activityData, _ := data["activity_data"].(string)
	if !strings.Contains(activityData, `"status":200`) {
		t.Errorf("expected status 200 in activity data, got: %s", activityData)
	}
}

func firstNonEmptyLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

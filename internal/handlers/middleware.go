package handlers

import (
	"net/http"
	"time"

	"github.com/remiges-tech/logharbour/logharbour"
)

// statusRecorder wraps http.ResponseWriter to capture the status code
// written, since net/http gives no other way to observe it after the
// fact. If the handler never calls WriteHeader explicitly (writing body
// bytes directly triggers an implicit 200), status stays at its zero
// value until Write/WriteHeader is observed, matching net/http's own
// "no WriteHeader call means 200" behavior.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// LoggingMiddleware logs every request that reaches the server — method,
// path, remote address, response status, and duration — at Info priority,
// so a hit is visible in the console at the default log level, not only
// when LOG_LEVEL=debug. This is the request-level access log; handlers
// that also know Beckn-specific context (transactionId, messageId, ...)
// log that themselves (see internal/service.DiscoverService.ProcessAsync).
func LoggingMiddleware(logger *logharbour.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}

			next.ServeHTTP(rec, r)

			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}

			logger.LogActivity("http request", map[string]any{
				"method":     r.Method,
				"path":       r.URL.Path,
				"remoteAddr": r.RemoteAddr,
				"status":     status,
				"durationMs": time.Since(start).Milliseconds(),
			})
		})
	}
}

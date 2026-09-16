package handlers

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/remiges-tech/logharbour/logharbour"
	"golang.org/x/time/rate"

	"github.com/remiges-tushar/discovery-service/internal/constants"
)

// staleClientTTL is how long a per-client limiter may sit idle before
// cleanup evicts it — otherwise every distinct address that ever calls in
// stays in RateLimiter.clients forever, an unbounded-memory-growth issue of
// exactly the kind CONTEXT.md D9 already hardened elsewhere in this
// service (worker pool, request body size, HTTP timeouts).
const staleClientTTL = 5 * time.Minute

// RateLimiter bounds how often a single client (keyed by remote address)
// may call the endpoint(s) it wraps. Necessary because inbound requests
// aren't authenticated yet (D15): without this, nothing bounds how many
// requests — each now doing real intent-matching work against the catalog
// (D19) — a single caller can issue. See CONTEXT.md D20.
type RateLimiter struct {
	requestsPerSecond rate.Limit
	burst             int
	logger            *logharbour.Logger

	mu      sync.Mutex
	clients map[string]*clientLimiter
}

type clientLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// NewRateLimiter builds a RateLimiter allowing requestsPerSecond sustained
// requests per client, with burst allowed instantaneously. It starts its
// own background cleanup goroutine that runs for the lifetime of the
// process (this service has no shutdown hook for it, the same as the
// package-level embedded-catalog init — acceptable since it holds no
// resources needing an orderly close, only an in-memory map).
func NewRateLimiter(requestsPerSecond float64, burst int, logger *logharbour.Logger) *RateLimiter {
	rl := &RateLimiter{
		requestsPerSecond: rate.Limit(requestsPerSecond),
		burst:             burst,
		logger:            logger,
		clients:           make(map[string]*clientLimiter),
	}
	go rl.cleanupLoop()
	return rl
}

// Middleware rejects a request with 429 (NackTooManyRequests) if the
// calling client (see clientKey) has exceeded its budget, otherwise passes
// it through unchanged.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.allow(clientKey(r)) {
			rl.logger.Warn().LogActivity("discover request rejected: rate limit exceeded", map[string]any{
				"remoteAddr": r.RemoteAddr,
			})
			writeNack(w, http.StatusTooManyRequests, constants.ErrTooManyRequests, "too many requests, slow down")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (rl *RateLimiter) allow(key string) bool {
	rl.mu.Lock()
	c, ok := rl.clients[key]
	if !ok {
		c = &clientLimiter{limiter: rate.NewLimiter(rl.requestsPerSecond, rl.burst)}
		rl.clients[key] = c
	}
	c.lastSeen = time.Now()
	rl.mu.Unlock()

	return c.limiter.Allow()
}

// cleanupLoop periodically evicts limiters for clients that haven't been
// seen in staleClientTTL, bounding rl.clients' memory regardless of how
// many distinct addresses have ever called in.
func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(staleClientTTL)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-staleClientTTL)
		rl.mu.Lock()
		for key, c := range rl.clients {
			if c.lastSeen.Before(cutoff) {
				delete(rl.clients, key)
			}
		}
		rl.mu.Unlock()
	}
}

// clientKey extracts the host part of r.RemoteAddr (dropping the ephemeral
// port, which differs per connection from the same client) to key a
// client's limiter. Falls back to the raw RemoteAddr if it isn't in
// host:port form.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Package config loads discovery-service's runtime configuration from
// environment variables (populated locally via .env — see .env.example at
// the repo root). Every configurable value has a sane default so the
// service runs with zero configuration.
package config

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/remiges-tushar/discovery-service/internal/constants"
)

const (
	defaultPort                    = "8080"
	defaultLogLevel                = "info"
	defaultDispatchClientTimeout   = 10 * time.Second
	defaultDispatchDeliveryTimeout = 10 * time.Second

	// defaultWorkerPoolSize/QueueSize bound the background on_discover
	// dispatch pool (internal/worker) — see CONTEXT.md D9. These are
	// deliberately modest for a single-instance M1 deployment; raise them
	// with real traffic data rather than guessing.
	defaultWorkerPoolSize  = 10
	defaultWorkerQueueSize = 100

	// defaultMaxRequestBodyBytes caps a single /discover request body.
	defaultMaxRequestBodyBytes = 1 << 20 // 1 MiB

	// HTTP server timeouts. Unset, Go's http.Server has NO timeouts at
	// all on these — a classic footgun that lets a slow/malicious client
	// hold a connection (and its goroutine) open indefinitely. Explicit
	// defaults here close that gap.
	defaultHTTPReadHeaderTimeout = 5 * time.Second
	defaultHTTPReadTimeout       = 10 * time.Second
	defaultHTTPWriteTimeout      = 10 * time.Second
	defaultHTTPIdleTimeout       = 60 * time.Second

	// defaultShutdownTimeout bounds how long graceful shutdown waits for
	// in-flight HTTP requests and background dispatch jobs to drain
	// before giving up — see CONTEXT.md D9.
	defaultShutdownTimeout = 15 * time.Second

	// defaultSigningKeyID/Validity — see Config.SigningKeyID/SigningValidity
	// and CONTEXT.md D15.
	defaultSigningKeyID    = "key-1"
	defaultSigningValidity = 5 * time.Minute

	// defaultCatalogRefreshInterval/FetchTimeout — see
	// Config.CatalogRefreshInterval/CatalogFetchTimeout and CONTEXT.md D17.
	defaultCatalogRefreshInterval = 60 * time.Second
	defaultCatalogFetchTimeout    = 10 * time.Second

	// defaultCatalogSourceMode preserves today's behavior (internal
	// dashboard-API polling) unless a deployment opts into "dedi" — see
	// Config.CatalogSourceMode and CONTEXT.md D18.
	defaultCatalogSourceMode = "dashboard"

	// defaultDediCutoverFraction matches dedicrawl's own
	// defaultCutoverFraction.
	defaultDediCutoverFraction = 0.5

	// defaultDediRegistryMode preserves today's fixture-only behavior
	// unless a deployment opts into "http" — see Config.DediRegistryMode
	// and CONTEXT.md D21.
	defaultDediRegistryMode = "fixture"
	// defaultDediRegistryURL is the confirmed-reachable public DeDi
	// registry — see CONTEXT.md D21.
	defaultDediRegistryURL = "https://fabric.nfh.global/registry/dedi"

	// defaultRateLimitRequestsPerSecond/Burst — see
	// Config.RateLimitRequestsPerSecond/RateLimitBurst and CONTEXT.md D20.
	// Comfortably above any single real BAP's expected call rate or the
	// demo catalogsource traffic pattern, while still bounding a runaway
	// caller.
	defaultRateLimitRequestsPerSecond = 5.0
	defaultRateLimitBurst             = 10

	// defaultMatchTimeout bounds a single request's intent matching
	// (matchCatalogs) — see Config.MatchTimeout and CONTEXT.md D20. A
	// filter is expected to complete in well under this.
	defaultMatchTimeout = 500 * time.Millisecond
)

// Config holds all runtime-configurable values for the service.
type Config struct {
	// Port the HTTP server listens on inside the container/process.
	Port string
	// DispatchClientTimeout bounds the underlying HTTP client used to
	// deliver on_discover callbacks.
	DispatchClientTimeout time.Duration
	// DispatchDeliveryTimeout bounds a single on_discover delivery attempt
	// via a context deadline.
	DispatchDeliveryTimeout time.Duration

	// WorkerPoolSize is the number of goroutines running background
	// on_discover dispatch jobs concurrently.
	WorkerPoolSize int
	// WorkerQueueSize is how many dispatch jobs may be queued waiting for
	// a free worker before new ones are dropped (with a logged warning).
	WorkerQueueSize int

	// MaxRequestBodyBytes caps how much of a /discover request body is
	// read before it's rejected.
	MaxRequestBodyBytes int64

	// HTTP server timeouts — see defaultHTTP* above.
	HTTPReadHeaderTimeout time.Duration
	HTTPReadTimeout       time.Duration
	HTTPWriteTimeout      time.Duration
	HTTPIdleTimeout       time.Duration

	// ShutdownTimeout bounds graceful shutdown drain time.
	ShutdownTimeout time.Duration

	// LogLevel selects the minimum logharbour priority to emit — see
	// internal/logging.ParsePriority for accepted values
	// (debug2/debug1/debug0|debug/info/warn/err|error/crit/sec). Setting
	// this to a debug tier also enables per-request "hit" logging.
	LogLevel string

	// BppID/BppURI are this service's own registered Beckn subscriber
	// identity. Stamped onto the on_discover response context (and any
	// catalog that doesn't already declare its own) whenever an incoming
	// /discover request doesn't already address a specific bppId/bppUri
	// — see service.BuildFixedOnDiscover and CONTEXT.md D13. Empty by
	// default; set these once this service is actually registered on a
	// network.
	BppID  string
	BppURI string

	// OnDiscoverPath is appended to a BAP's context.bapUri to form the
	// on_discover callback URL. Defaults to the spec-correct
	// "/on_discover" (constants.OnDiscoverPath). Override via
	// ON_DISCOVER_PATH only when a specific counterpart's registered
	// bapUri doesn't already include its full receiver route (an
	// observed real-world case with an ONIX bapTxnReceiver expecting
	// "/bap/receiver/on_discover") — see CONTEXT.md D14. This is a
	// stopgap: the durable fix is resolving the real callback URL from a
	// registry lookup instead of trusting/guessing at bapUri (D3/M4).
	OnDiscoverPath string

	// SigningPrivateKeyBase64 is a base64-encoded 32-byte Ed25519 seed
	// used to sign outbound requests (see internal/signing, CONTEXT.md
	// D15). Empty by default: main.go generates a fresh ephemeral key
	// pair on every start instead, logs its public key, and signs with
	// that — good enough to stop a real BAP rejecting us for a missing
	// signature, but nobody can actually verify it against a registry
	// entry until a real persistent key is configured here and published.
	SigningPrivateKeyBase64 string
	// SigningKeyID is the "{uniqueKeyId}" segment of the signature's
	// keyId (see beckn.FormatSignature).
	SigningKeyID string
	// SigningValidity is how long a signature is valid for
	// (created→expires).
	SigningValidity time.Duration

	// CatalogSourceURLs is a list of BPP base URLs (e.g.
	// "http://bpp-application:8080" in Docker, a Cloud Run URL in
	// production) discovery-service polls for real catalog data — see
	// CONTEXT.md D17. Each entry must expose the same dashboard-style
	// GET /api/v1/catalogs and GET /api/v1/catalogs/{id} endpoints
	// internal/catalogsource maps into beckn.Catalog. Empty by default:
	// with no sources configured, the embedded demo catalog
	// (data/catalog.json) is served instead, unchanged from M1.
	CatalogSourceURLs []string
	// CatalogRefreshInterval is how often each configured source is
	// re-polled in the background after the initial fetch.
	CatalogRefreshInterval time.Duration
	// CatalogFetchTimeout bounds a single HTTP call (list or detail) to a
	// catalog source.
	CatalogFetchTimeout time.Duration

	// CatalogSourceMode selects which catalog-ingestion mechanism
	// buildCatalogStore (cmd/discovery/main.go) wires up: "dashboard"
	// (default — internal/catalogsource, unchanged, CONTEXT.md D17) or
	// "dedi" (internal/dedicrawl, the protocol-specifications-v2 §10
	// decentralized crawler — see CONTEXT.md D18). Any other/unset value
	// behaves as "dashboard".
	CatalogSourceMode string

	// DediSubscriberRefs is the list of Provider Node subscriber
	// references dedicrawl.Crawler crawls when CatalogSourceMode is
	// "dedi". Ignored otherwise.
	DediSubscriberRefs []string
	// DediNetworkIDs/DediSchemaTypes optionally scope which catalog-index
	// entries get indexed at all (dedicrawl.Crawler.inScope). Empty means
	// "accept everything".
	DediNetworkIDs  []string
	DediSchemaTypes []string
	// DediCutoverFraction is the §10.1 cutover rule's threshold — see
	// dedicrawl.NewCrawler.
	DediCutoverFraction float64
	// DediFixturePath points at a checked-in dev/demo fixture (see
	// internal/dedicrawl/fixture.go) used to seed a dedicrawl.StubRegistry
	// with self-signed catalog data. Only read when DediRegistryMode is
	// "fixture" (the default). Required for that mode to serve anything;
	// if empty or unreadable, buildCatalogStore logs a warning and falls
	// back to service.EmbeddedCatalogStore{} rather than starting with an
	// empty catalog.
	DediFixturePath string
	// DediRegistryMode selects which dedicrawl.Registry backend "dedi"
	// catalog-source mode uses: "fixture" (default — DediFixturePath's
	// self-signed dev/demo data, no real registry involved) or "http" (a
	// real dedicrawl.HTTPRegistry against DediRegistryURL — see
	// CONTEXT.md D21; confirmed reachable, but requires a real, live
	// subscriberRef in DediSubscriberRefs to have anything to crawl).
	DediRegistryMode string
	// DediRegistryURL is the real DeDi registry HTTPRegistry queries when
	// DediRegistryMode is "http". Defaults to the confirmed-reachable
	// public registry at fabric.nfh.global; only actually called when
	// "http" mode is selected.
	DediRegistryURL string

	// RateLimitEnabled/RequestsPerSecond/Burst bound how often a single
	// client (keyed by remote address) may call /discover — see
	// internal/handlers.RateLimiter and CONTEXT.md D20. Necessary because
	// inbound requests aren't authenticated yet (D15): without some limit,
	// nothing bounds how many requests, each now doing real matching work
	// (D19), a single caller can issue.
	RateLimitEnabled           bool
	RateLimitRequestsPerSecond float64
	RateLimitBurst             int

	// MatchTimeout bounds how long a single request's intent matching
	// (service.matchCatalogs) is allowed to run before it's cut short and
	// whatever's matched so far is returned — see CONTEXT.md D20. Protects
	// against one expensive filter expression even from a client that's
	// within its rate limit.
	MatchTimeout time.Duration
}

// Addr returns the listen address (":<port>") for http.ListenAndServe.
func (c Config) Addr() string {
	return ":" + c.Port
}

// Load reads configuration from the environment, falling back to defaults
// for anything unset or invalid. Before reading, it applies a .env file
// found in the working directory or one of its ancestors (see
// loadDotEnv) — real environment variables always take precedence.
func Load() Config {
	loadDotEnv()

	return Config{
		Port:                    getString("PORT", defaultPort),
		DispatchClientTimeout:   getSeconds("DISPATCH_CLIENT_TIMEOUT_SECONDS", defaultDispatchClientTimeout),
		DispatchDeliveryTimeout: getSeconds("DISPATCH_DELIVERY_TIMEOUT_SECONDS", defaultDispatchDeliveryTimeout),

		WorkerPoolSize:  getInt("WORKER_POOL_SIZE", defaultWorkerPoolSize),
		WorkerQueueSize: getInt("WORKER_QUEUE_SIZE", defaultWorkerQueueSize),

		MaxRequestBodyBytes: getInt64("MAX_REQUEST_BODY_BYTES", defaultMaxRequestBodyBytes),

		HTTPReadHeaderTimeout: getSeconds("HTTP_READ_HEADER_TIMEOUT_SECONDS", defaultHTTPReadHeaderTimeout),
		HTTPReadTimeout:       getSeconds("HTTP_READ_TIMEOUT_SECONDS", defaultHTTPReadTimeout),
		HTTPWriteTimeout:      getSeconds("HTTP_WRITE_TIMEOUT_SECONDS", defaultHTTPWriteTimeout),
		HTTPIdleTimeout:       getSeconds("HTTP_IDLE_TIMEOUT_SECONDS", defaultHTTPIdleTimeout),

		ShutdownTimeout: getSeconds("SHUTDOWN_TIMEOUT_SECONDS", defaultShutdownTimeout),

		LogLevel: getString("LOG_LEVEL", defaultLogLevel),

		BppID:  getString("BPP_ID", ""),
		BppURI: getString("BPP_URI", ""),

		OnDiscoverPath: getString("ON_DISCOVER_PATH", constants.OnDiscoverPath),

		SigningPrivateKeyBase64: getString("SIGNING_PRIVATE_KEY_BASE64", ""),
		SigningKeyID:            getString("SIGNING_KEY_ID", defaultSigningKeyID),
		SigningValidity:         getSeconds("SIGNING_VALIDITY_SECONDS", defaultSigningValidity),

		CatalogSourceURLs:      getStringSlice("CATALOG_SOURCE_URLS"),
		CatalogRefreshInterval: getSeconds("CATALOG_REFRESH_INTERVAL_SECONDS", defaultCatalogRefreshInterval),
		CatalogFetchTimeout:    getSeconds("CATALOG_FETCH_TIMEOUT_SECONDS", defaultCatalogFetchTimeout),

		CatalogSourceMode:   getString("CATALOG_SOURCE_MODE", defaultCatalogSourceMode),
		DediSubscriberRefs:  getStringSlice("DEDI_SUBSCRIBER_REFS"),
		DediNetworkIDs:      getStringSlice("DEDI_NETWORK_IDS"),
		DediSchemaTypes:     getStringSlice("DEDI_SCHEMA_TYPES"),
		DediCutoverFraction: getFloat("DEDI_CUTOVER_FRACTION", defaultDediCutoverFraction),
		DediFixturePath:     getString("DEDI_FIXTURE_PATH", ""),
		DediRegistryMode:    getString("DEDI_REGISTRY_MODE", defaultDediRegistryMode),
		DediRegistryURL:     getString("DEDI_REGISTRY_URL", defaultDediRegistryURL),

		RateLimitEnabled:           getBool("RATE_LIMIT_ENABLED", true),
		RateLimitRequestsPerSecond: getFloat("RATE_LIMIT_REQUESTS_PER_SECOND", defaultRateLimitRequestsPerSecond),
		RateLimitBurst:             getInt("RATE_LIMIT_BURST", defaultRateLimitBurst),

		MatchTimeout: getMillis("MATCH_TIMEOUT_MILLISECONDS", defaultMatchTimeout),
	}
}

// getStringSlice parses a comma-separated env var into a trimmed,
// non-empty-entry slice. Returns nil (not an empty non-nil slice) when the
// var is unset or contains only empty/whitespace entries, so callers can
// treat "unset" and "explicitly empty" the same way (len(...) == 0).
func getStringSlice(key string) []string {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	var out []string
	for part := range strings.SplitSeq(v, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func getString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getSeconds(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	seconds, err := strconv.Atoi(v)
	if err != nil {
		slog.Warn("invalid duration env var, using default", "key", key, "value", v, "default", fallback)
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func getInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		slog.Warn("invalid integer env var, using default", "key", key, "value", v, "default", fallback)
		return fallback
	}
	return n
}

func getFloat(key string, fallback float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		slog.Warn("invalid float env var, using default", "key", key, "value", v, "default", fallback)
		return fallback
	}
	return f
}

func getBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		slog.Warn("invalid boolean env var, using default", "key", key, "value", v, "default", fallback)
		return fallback
	}
	return b
}

// getMillis parses key as an integer count of milliseconds — finer-grained
// than getSeconds, for timeouts (like MatchTimeout) expected to be well
// under a second.
func getMillis(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	millis, err := strconv.Atoi(v)
	if err != nil {
		slog.Warn("invalid integer env var, using default", "key", key, "value", v, "default", fallback)
		return fallback
	}
	return time.Duration(millis) * time.Millisecond
}

func getInt64(key string, fallback int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		slog.Warn("invalid integer env var, using default", "key", key, "value", v, "default", fallback)
		return fallback
	}
	return n
}

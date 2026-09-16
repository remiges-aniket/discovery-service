// Command discovery runs the Beckn v2 Discovery Service HTTP server.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/remiges-tech/logharbour/logharbour"
	"github.com/remiges-tushar/discovery-service/internal/catalogsource"
	"github.com/remiges-tushar/discovery-service/internal/config"
	"github.com/remiges-tushar/discovery-service/internal/dedicrawl"
	"github.com/remiges-tushar/discovery-service/internal/dispatch"
	"github.com/remiges-tushar/discovery-service/internal/handlers"
	"github.com/remiges-tushar/discovery-service/internal/logging"
	"github.com/remiges-tushar/discovery-service/internal/service"
	"github.com/remiges-tushar/discovery-service/internal/signing"
	"github.com/remiges-tushar/discovery-service/internal/worker"
)

func main() {
	cfg := config.Load()

	priority := logging.ParsePriority(cfg.LogLevel)
	logger := logging.New("discovery-service", priority, logging.IsDebugLevel(priority))

	dispatchSigner := buildSigner(cfg, logger)

	pool := worker.NewPool(cfg.WorkerPoolSize, cfg.WorkerQueueSize, logger)
	dispatcher := dispatch.NewHTTPDispatcher(&http.Client{Timeout: cfg.DispatchClientTimeout}, cfg.OnDiscoverPath, dispatchSigner)

	catalogStore, stopCatalogRefresh := buildCatalogStore(cfg, logger)
	defer stopCatalogRefresh()

	svc := service.NewDiscoverService(dispatcher, pool, cfg.DispatchDeliveryTimeout, logger, cfg.BppID, cfg.BppURI, catalogStore, cfg.MatchTimeout)
	discoverHandler := handlers.NewDiscoverHandler(svc, cfg.MaxRequestBodyBytes, logger)

	mux := http.NewServeMux()
	if cfg.RateLimitEnabled {
		rateLimiter := handlers.NewRateLimiter(cfg.RateLimitRequestsPerSecond, cfg.RateLimitBurst, logger)
		mux.Handle("POST /discover", rateLimiter.Middleware(discoverHandler))
		mux.Handle("GET /discover", rateLimiter.Middleware(http.HandlerFunc(discoverHandler.ServeSync)))
	} else {
		mux.Handle("POST /discover", discoverHandler)
		mux.HandleFunc("GET /discover", discoverHandler.ServeSync)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// LoggingMiddleware wraps every route (including /healthz) so every
	// hit is visible in the console at the default log level — see
	// CONTEXT.md D10. DiscoverHandler additionally logs Beckn-specific
	// context (transactionId, messageId, ...) on top of this.
	var handler http.Handler = mux
	handler = handlers.LoggingMiddleware(logger)(handler)

	// Explicit timeouts: an http.Server with none set will hold a slow or
	// malicious client's connection (and the goroutine serving it) open
	// indefinitely — a classic resource-exhaustion footgun.
	srv := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           handler,
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
		ReadTimeout:       cfg.HTTPReadTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.LogActivity("discovery-service listening", map[string]any{
			"addr":     cfg.Addr(),
			"logLevel": cfg.LogLevel,
		})
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	// Graceful shutdown: stop accepting new connections on SIGINT/SIGTERM
	// (Ctrl+C locally, or what `docker compose down` / a k8s pod eviction
	// actually sends), then drain both in-flight HTTP requests and queued
	// on_discover dispatch jobs before exiting — bounded by
	// cfg.ShutdownTimeout so a stuck job can't hang the process forever.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		if err != nil {
			logger.Crit().LogActivity("server exited unexpectedly", map[string]any{"error": err.Error()})
			os.Exit(1)
		}
		return
	case sig := <-stop:
		logger.LogActivity("shutdown signal received", map[string]any{"signal": sig.String()})
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Err().LogActivity("HTTP server did not shut down cleanly", map[string]any{"error": err.Error()})
	}
	if err := pool.Shutdown(shutdownCtx); err != nil {
		logger.Err().LogActivity("background dispatch pool did not drain in time", map[string]any{"error": err.Error()})
	}

	logger.LogActivity("discovery-service stopped", nil)
}

// buildSigner returns a signer for outbound requests (see CONTEXT.md
// D15), or nil if cfg.BppID is unset — signing without a subscriber
// identity would produce a Signature keyId with an empty first segment,
// which fails the spec's own pattern, and no receiver could attribute
// the signature to anyone anyway.
func buildSigner(cfg config.Config, logger *logharbour.Logger) dispatch.RequestSigner {
	if cfg.BppID == "" {
		logger.Warn().LogActivity("BPP_ID not configured — outbound on_discover requests will be unsigned", nil)
		return nil
	}

	kp, ephemeral, err := signing.LoadOrGenerateKeyPair(cfg.SigningPrivateKeyBase64)
	if err != nil {
		logger.Crit().LogActivity("failed to initialize signing key", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	logger.LogActivity("signing key initialized", map[string]any{
		"ephemeral":       ephemeral,
		"publicKeyBase64": base64.StdEncoding.EncodeToString(kp.Public),
		"bppId":           cfg.BppID,
		"keyId":           cfg.SigningKeyID,
	})
	if ephemeral {
		logger.Warn().LogActivity("using an ephemeral signing key — nothing can verify these signatures until a persistent SIGNING_PRIVATE_KEY_BASE64 is configured and its public key published to a registry", nil)
	}

	return signing.NewSigner(kp, cfg.BppID, cfg.SigningKeyID, cfg.SigningValidity)
}

// buildCatalogStore returns the CatalogStore /discover serves from, and a
// stop function to cancel any background refresh goroutine it started
// (call it during shutdown; safe to call even when nothing was started).
//
// cfg.CatalogSourceMode selects the mechanism (see CONTEXT.md D17/D18):
//   - "dashboard" (default): unchanged from before this switch existed. With
//     no CATALOG_SOURCE_URLS configured, returns the zero-config embedded
//     demo catalog (service.EmbeddedCatalogStore). Otherwise builds a
//     catalogsource.Store polling each configured BPP's dashboard API.
//   - "dedi": builds a dedicrawl.Store crawling Provider Nodes per
//     protocol-specifications-v2 §10. No real, reachable DeDi Registry
//     exists yet, so this mode only has data to serve when
//     cfg.DediFixturePath points at a dev/demo fixture (see
//     dedicrawl.SeedFixture); with no usable fixture it logs a warning and
//     falls back to the embedded demo catalog rather than starting with
//     nothing.
//
// Both branches do one synchronous Refresh now (so the first real request
// already sees fresh data if reachable) then start background polling at
// CatalogRefreshInterval.
func buildCatalogStore(cfg config.Config, logger *logharbour.Logger) (service.CatalogStore, func()) {
	if cfg.CatalogSourceMode == "dedi" {
		return buildDediCatalogStore(cfg, logger)
	}
	return buildDashboardCatalogStore(cfg, logger)
}

func buildDashboardCatalogStore(cfg config.Config, logger *logharbour.Logger) (service.CatalogStore, func()) {
	if len(cfg.CatalogSourceURLs) == 0 {
		return service.EmbeddedCatalogStore{}, func() {}
	}

	logger.LogActivity("catalog sources configured, polling for real catalog data", map[string]any{
		"sources":         cfg.CatalogSourceURLs,
		"refreshInterval": cfg.CatalogRefreshInterval.String(),
	})

	source := catalogsource.NewHTTPSource(cfg.CatalogSourceURLs, &http.Client{Timeout: cfg.CatalogFetchTimeout}, logger)
	store := catalogsource.NewStore(source, cfg.CatalogRefreshInterval, logger)

	initialCtx, initialCancel := context.WithTimeout(context.Background(), cfg.CatalogFetchTimeout*time.Duration(len(cfg.CatalogSourceURLs)+1))
	store.Refresh(initialCtx)
	initialCancel()

	refreshCtx, stopRefresh := context.WithCancel(context.Background())
	go store.Start(refreshCtx)

	return store, stopRefresh
}

// buildDediCatalogStore wires the internal/dedicrawl §10 crawler in place
// of catalogsource, using one of two dedicrawl.Registry backends selected
// by cfg.DediRegistryMode (see CONTEXT.md D18/D21):
//   - "fixture" (default): dedicrawl.StubRegistry seeded from a checked-in
//     dev/demo fixture file (cfg.DediFixturePath) — see dedicrawl.SeedFixture.
//   - "http": dedicrawl.HTTPRegistry against a real, live DeDi registry
//     (cfg.DediRegistryURL — confirmed reachable). Requires a real,
//     registered subscriberRef in cfg.DediSubscriberRefs to have anything
//     to crawl; there's no fixture-seeded fallback list in this mode.
func buildDediCatalogStore(cfg config.Config, logger *logharbour.Logger) (service.CatalogStore, func()) {
	var registry dedicrawl.Registry
	var subscriberRefs []string
	stopRegistry := func() {}

	if cfg.DediRegistryMode == "http" {
		if len(cfg.DediSubscriberRefs) == 0 {
			logger.Warn().LogActivity("CATALOG_SOURCE_MODE=dedi, DEDI_REGISTRY_MODE=http but DEDI_SUBSCRIBER_REFS is unset — nothing configured to crawl, falling back to the embedded demo catalog", nil)
			return service.EmbeddedCatalogStore{}, func() {}
		}
		httpRegistry, err := dedicrawl.NewHTTPRegistry(cfg.DediRegistryURL, &http.Client{Timeout: cfg.CatalogFetchTimeout})
		if err != nil {
			logger.Err().LogActivity("failed to build HTTP dedi registry client, falling back to the embedded demo catalog", map[string]any{"error": err.Error(), "registryUrl": cfg.DediRegistryURL})
			return service.EmbeddedCatalogStore{}, func() {}
		}
		registry = httpRegistry
		subscriberRefs = cfg.DediSubscriberRefs
		logger.LogActivity("dedi catalog source configured against a real registry, crawling for catalog data", map[string]any{
			"registryUrl":     cfg.DediRegistryURL,
			"subscriberRefs":  subscriberRefs,
			"refreshInterval": cfg.CatalogRefreshInterval.String(),
		})
	} else {
		if cfg.DediFixturePath == "" {
			logger.Warn().LogActivity("CATALOG_SOURCE_MODE=dedi, DEDI_REGISTRY_MODE=fixture but DEDI_FIXTURE_PATH is unset, falling back to the embedded demo catalog", nil)
			return service.EmbeddedCatalogStore{}, func() {}
		}

		fx, err := dedicrawl.LoadFixture(cfg.DediFixturePath)
		if err != nil {
			logger.Err().LogActivity("failed to load dedi fixture, falling back to the embedded demo catalog", map[string]any{"error": err.Error(), "path": cfg.DediFixturePath})
			return service.EmbeddedCatalogStore{}, func() {}
		}

		stubRegistry, fixtureRefs, stopFixture, err := dedicrawl.SeedFixture(fx)
		if err != nil {
			logger.Err().LogActivity("failed to seed dedi fixture, falling back to the embedded demo catalog", map[string]any{"error": err.Error(), "path": cfg.DediFixturePath})
			return service.EmbeddedCatalogStore{}, func() {}
		}
		registry = stubRegistry
		stopRegistry = stopFixture

		// DediSubscriberRefs lets an operator crawl only a subset of what
		// the fixture declares; unset means "everything the fixture seeded".
		subscriberRefs = cfg.DediSubscriberRefs
		if len(subscriberRefs) == 0 {
			subscriberRefs = fixtureRefs
		}

		logger.LogActivity("dedi catalog source configured from fixture, crawling for catalog data", map[string]any{
			"fixturePath":     cfg.DediFixturePath,
			"subscriberRefs":  subscriberRefs,
			"refreshInterval": cfg.CatalogRefreshInterval.String(),
		})
	}

	crawler := dedicrawl.NewCrawler(registry, "", &http.Client{Timeout: cfg.CatalogFetchTimeout}, logger, cfg.DediNetworkIDs, cfg.DediSchemaTypes, cfg.DediCutoverFraction)
	store := dedicrawl.NewStore(crawler, subscriberRefs, cfg.CatalogRefreshInterval, logger)

	initialCtx, initialCancel := context.WithTimeout(context.Background(), cfg.CatalogFetchTimeout*time.Duration(len(subscriberRefs)+1))
	store.Refresh(initialCtx)
	initialCancel()

	refreshCtx, stopRefresh := context.WithCancel(context.Background())
	go store.Start(refreshCtx)

	return store, func() {
		stopRefresh()
		stopRegistry()
	}
}

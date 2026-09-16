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

	svc := service.NewDiscoverService(dispatcher, pool, cfg.DispatchDeliveryTimeout, logger, cfg.BppID, cfg.BppURI, catalogStore)
	discoverHandler := handlers.NewDiscoverHandler(svc, cfg.MaxRequestBodyBytes, logger)

	mux := http.NewServeMux()
	mux.Handle("POST /discover", discoverHandler)
	mux.HandleFunc("GET /discover", discoverHandler.ServeSync)
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
// With no CATALOG_SOURCE_URLS configured, it returns the zero-config
// embedded demo catalog (service.EmbeddedCatalogStore), unchanged from
// M1. Otherwise it builds a catalogsource.Store, does one synchronous
// Refresh now (bounded by CatalogFetchTimeout per HTTP call) so the first
// real requests already see fresh data if the configured sources are
// reachable, then starts background polling at CatalogRefreshInterval —
// see CONTEXT.md D17.
func buildCatalogStore(cfg config.Config, logger *logharbour.Logger) (service.CatalogStore, func()) {
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

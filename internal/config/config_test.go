package config

import (
	"testing"
	"time"
)

func TestLoad_Defaults_WhenNoEnvVarsSet(t *testing.T) {
	// Isolate from this repo's own real .env (which legitimately overrides
	// PORT, per the dotenv tests below) — "defaults" must mean defaults,
	// regardless of what .env happens to contain wherever tests run from.
	t.Chdir(t.TempDir())

	cfg := Load()

	if cfg.Port != "8080" {
		t.Errorf("Port = %q, want 8080", cfg.Port)
	}
	if cfg.DispatchClientTimeout != 10*time.Second {
		t.Errorf("DispatchClientTimeout = %v, want 10s", cfg.DispatchClientTimeout)
	}
	if cfg.DispatchDeliveryTimeout != 10*time.Second {
		t.Errorf("DispatchDeliveryTimeout = %v, want 10s", cfg.DispatchDeliveryTimeout)
	}
	if cfg.WorkerPoolSize != 10 {
		t.Errorf("WorkerPoolSize = %d, want 10", cfg.WorkerPoolSize)
	}
	if cfg.WorkerQueueSize != 100 {
		t.Errorf("WorkerQueueSize = %d, want 100", cfg.WorkerQueueSize)
	}
	if cfg.MaxRequestBodyBytes != 1<<20 {
		t.Errorf("MaxRequestBodyBytes = %d, want %d", cfg.MaxRequestBodyBytes, 1<<20)
	}
	if cfg.HTTPReadHeaderTimeout != 5*time.Second {
		t.Errorf("HTTPReadHeaderTimeout = %v, want 5s", cfg.HTTPReadHeaderTimeout)
	}
	if cfg.HTTPReadTimeout != 10*time.Second {
		t.Errorf("HTTPReadTimeout = %v, want 10s", cfg.HTTPReadTimeout)
	}
	if cfg.HTTPWriteTimeout != 10*time.Second {
		t.Errorf("HTTPWriteTimeout = %v, want 10s", cfg.HTTPWriteTimeout)
	}
	if cfg.HTTPIdleTimeout != 60*time.Second {
		t.Errorf("HTTPIdleTimeout = %v, want 60s", cfg.HTTPIdleTimeout)
	}
	if cfg.ShutdownTimeout != 15*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 15s", cfg.ShutdownTimeout)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", cfg.LogLevel)
	}
	if cfg.BppID != "" {
		t.Errorf("BppID = %q, want empty by default", cfg.BppID)
	}
	if cfg.BppURI != "" {
		t.Errorf("BppURI = %q, want empty by default", cfg.BppURI)
	}
	if cfg.OnDiscoverPath != "/on_discover" {
		t.Errorf("OnDiscoverPath = %q, want the spec-correct default /on_discover", cfg.OnDiscoverPath)
	}
	if cfg.SigningPrivateKeyBase64 != "" {
		t.Errorf("SigningPrivateKeyBase64 = %q, want empty by default", cfg.SigningPrivateKeyBase64)
	}
	if cfg.SigningKeyID != "key-1" {
		t.Errorf("SigningKeyID = %q, want key-1", cfg.SigningKeyID)
	}
	if cfg.SigningValidity != 5*time.Minute {
		t.Errorf("SigningValidity = %v, want 5m", cfg.SigningValidity)
	}
}

func TestLoad_ReadsOverridesFromEnv(t *testing.T) {
	t.Chdir(t.TempDir()) // isolate from this repo's own .env, see above
	t.Setenv("PORT", "9090")
	t.Setenv("DISPATCH_CLIENT_TIMEOUT_SECONDS", "3")
	t.Setenv("DISPATCH_DELIVERY_TIMEOUT_SECONDS", "7")
	t.Setenv("WORKER_POOL_SIZE", "4")
	t.Setenv("WORKER_QUEUE_SIZE", "50")
	t.Setenv("MAX_REQUEST_BODY_BYTES", "2048")
	t.Setenv("SHUTDOWN_TIMEOUT_SECONDS", "20")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("BPP_ID", "ds-fabric.ion.id")
	t.Setenv("BPP_URI", "https://discover.infra.ion.id")
	t.Setenv("ON_DISCOVER_PATH", "/bap/receiver/on_discover")
	t.Setenv("SIGNING_PRIVATE_KEY_BASE64", "c29tZS1iYXNlNjQtdmFsdWU=")
	t.Setenv("SIGNING_KEY_ID", "key-2")
	t.Setenv("SIGNING_VALIDITY_SECONDS", "60")

	cfg := Load()

	if cfg.Port != "9090" {
		t.Errorf("Port = %q, want 9090", cfg.Port)
	}
	if cfg.DispatchClientTimeout != 3*time.Second {
		t.Errorf("DispatchClientTimeout = %v, want 3s", cfg.DispatchClientTimeout)
	}
	if cfg.DispatchDeliveryTimeout != 7*time.Second {
		t.Errorf("DispatchDeliveryTimeout = %v, want 7s", cfg.DispatchDeliveryTimeout)
	}
	if cfg.WorkerPoolSize != 4 {
		t.Errorf("WorkerPoolSize = %d, want 4", cfg.WorkerPoolSize)
	}
	if cfg.WorkerQueueSize != 50 {
		t.Errorf("WorkerQueueSize = %d, want 50", cfg.WorkerQueueSize)
	}
	if cfg.MaxRequestBodyBytes != 2048 {
		t.Errorf("MaxRequestBodyBytes = %d, want 2048", cfg.MaxRequestBodyBytes)
	}
	if cfg.ShutdownTimeout != 20*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 20s", cfg.ShutdownTimeout)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
	if cfg.BppID != "ds-fabric.ion.id" {
		t.Errorf("BppID = %q, want ds-fabric.ion.id", cfg.BppID)
	}
	if cfg.BppURI != "https://discover.infra.ion.id" {
		t.Errorf("BppURI = %q, want https://discover.infra.ion.id", cfg.BppURI)
	}
	if cfg.OnDiscoverPath != "/bap/receiver/on_discover" {
		t.Errorf("OnDiscoverPath = %q, want /bap/receiver/on_discover", cfg.OnDiscoverPath)
	}
	if cfg.SigningPrivateKeyBase64 != "c29tZS1iYXNlNjQtdmFsdWU=" {
		t.Errorf("SigningPrivateKeyBase64 = %q, want c29tZS1iYXNlNjQtdmFsdWU=", cfg.SigningPrivateKeyBase64)
	}
	if cfg.SigningKeyID != "key-2" {
		t.Errorf("SigningKeyID = %q, want key-2", cfg.SigningKeyID)
	}
	if cfg.SigningValidity != 60*time.Second {
		t.Errorf("SigningValidity = %v, want 60s", cfg.SigningValidity)
	}
}

func TestLoad_FallsBackToDefaultOnInvalidDuration(t *testing.T) {
	t.Chdir(t.TempDir()) // isolate from this repo's own .env, see above
	t.Setenv("DISPATCH_CLIENT_TIMEOUT_SECONDS", "not-a-number")

	cfg := Load()

	if cfg.DispatchClientTimeout != 10*time.Second {
		t.Errorf("DispatchClientTimeout = %v, want fallback default 10s", cfg.DispatchClientTimeout)
	}
}

func TestAddr_PrependsColonToPort(t *testing.T) {
	cfg := Config{Port: "8080"}
	if got := cfg.Addr(); got != ":8080" {
		t.Errorf("Addr() = %q, want :8080", got)
	}
}

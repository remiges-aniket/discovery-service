package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/remiges-tech/logharbour/logharbour"
	"github.com/remiges-tushar/discovery-service/internal/beckn"
	"github.com/remiges-tushar/discovery-service/internal/dispatch"
	"github.com/remiges-tushar/discovery-service/internal/service"
	"github.com/remiges-tushar/discovery-service/internal/worker"
)

func testLogger() *logharbour.Logger {
	ctx := logharbour.NewLoggerContext(logharbour.Info)
	return logharbour.NewLoggerWithFallback(ctx, "handlers-test", logharbour.NewFallbackWriter(io.Discard, io.Discard))
}

func validRequestBody(bapURI string) string {
	return `{
		"context": {
			"action": "discover",
			"version": "2.0.0",
			"bapId": "bap.example.com",
			"bapUri": "` + bapURI + `",
			"transactionId": "txn-1",
			"messageId": "msg-1"
		},
		"message": { "intent": { "textSearch": "laptop" } }
	}`
}

func newTestHandler(t *testing.T) *DiscoverHandler {
	client := &http.Client{Timeout: 2 * time.Second}
	pool := worker.NewPool(2, 4, testLogger())
	t.Cleanup(func() { _ = pool.Shutdown(context.Background()) })

	svc := service.NewDiscoverService(dispatch.NewHTTPDispatcher(client, "/on_discover", nil), pool, 2*time.Second, testLogger(), "own-bpp.example.com", "https://own-bpp.example.com", service.EmbeddedCatalogStore{})
	return NewDiscoverHandler(svc, testMaxBodyBytes, testLogger())
}

const testMaxBodyBytes = 1 << 20 // 1 MiB, mirrors config.defaultMaxRequestBodyBytes

func TestDiscoverHandler_ValidRequest_ReturnsAck(t *testing.T) {
	h := newTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/discover", strings.NewReader(validRequestBody("https://bap.example.com")))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var ack beckn.Ack
	if err := json.Unmarshal(rec.Body.Bytes(), &ack); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if ack.Status != "ACK" {
		t.Errorf("status = %q, want ACK", ack.Status)
	}
	if ack.Signature == "" {
		t.Error("expected a (placeholder) signature value on the Ack")
	}
	// The Ack must be JSON-decodable with Signature as a plain string —
	// if it were still (wrongly) serialized as a nested object, decoding
	// into beckn.Ack (Signature is now a string type) would have failed
	// above already, but assert the raw wire shape explicitly too.
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw ack: %v", err)
	}
	if _, isString := raw["signature"].(string); !isString {
		t.Errorf("wire-level signature field is not a JSON string (got %T) — beckn.yaml requires Signature to be type: string", raw["signature"])
	}
}

func TestDiscoverHandler_ValidRequest_DeliversOnDiscoverAsynchronously(t *testing.T) {
	received := make(chan beckn.OnDiscoverRequest, 1)
	bap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body beckn.OnDiscoverRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(beckn.Ack{Status: "ACK"})
		received <- body
	}))
	defer bap.Close()

	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/discover", strings.NewReader(validRequestBody(bap.URL)))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	select {
	case cb := <-received:
		if cb.Context.Action != "on_discover" {
			t.Errorf("callback context.action = %q, want on_discover", cb.Context.Action)
		}
		if cb.Context.TransactionID != "txn-1" {
			t.Errorf("callback transactionId = %q, want txn-1", cb.Context.TransactionID)
		}
		if len(cb.Message.Catalogs) == 0 {
			t.Error("expected at least one catalog in on_discover callback")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for on_discover callback")
	}
}

func TestDiscoverHandler_MissingTransactionID_ReturnsNack400(t *testing.T) {
	h := newTestHandler(t)
	body := `{"context":{"action":"discover","version":"2.0.0","bapUri":"https://bap.example.com","messageId":"m1"},"message":{"intent":{}}}`
	req := httptest.NewRequest(http.MethodPost, "/discover", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	var ack beckn.Ack
	if err := json.Unmarshal(rec.Body.Bytes(), &ack); err != nil {
		t.Fatalf("decode nack: %v", err)
	}
	if ack.Status != "NACK" {
		t.Errorf("status = %q, want NACK", ack.Status)
	}
	if ack.Error == nil || ack.Error.ErrorCode == "" {
		t.Error("expected a populated error on NACK")
	}
}

func TestDiscoverHandler_MalformedJSON_ReturnsNack400(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/discover", strings.NewReader("{not json"))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// GET /discover (see CONTEXT.md D16) returns the on_discover result
// inline, with no async callback involved at all.
func TestDiscoverHandler_ServeSync_ValidRequest_ReturnsOnDiscoverInline(t *testing.T) {
	h := newTestHandler(t)

	// No context.bapUri at all — a synchronous caller has nowhere for a
	// callback to go, and ValidateSync must not require one.
	body := `{
		"context": {
			"action": "discover",
			"version": "2.0.0",
			"transactionId": "txn-sync-1",
			"messageId": "msg-sync-1"
		},
		"message": { "intent": { "textSearch": "laptop" } }
	}`
	req := httptest.NewRequest(http.MethodGet, "/discover", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.ServeSync(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp beckn.OnDiscoverRequest
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode on_discover response: %v", err)
	}
	if resp.Context.Action != "on_discover" {
		t.Errorf("context.action = %q, want on_discover", resp.Context.Action)
	}
	if resp.Context.TransactionID != "txn-sync-1" {
		t.Errorf("transactionId = %q, want txn-sync-1", resp.Context.TransactionID)
	}
	if len(resp.Message.Catalogs) == 0 {
		t.Error("expected at least one catalog in the synchronous response")
	}
}

func TestDiscoverHandler_ServeSync_MissingBapUri_StillAccepted(t *testing.T) {
	h := newTestHandler(t)
	body := `{"context":{"transactionId":"t1","messageId":"m1"},"message":{"intent":{}}}`
	req := httptest.NewRequest(http.MethodGet, "/discover", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.ServeSync(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (bapUri must not be required on the sync path); body=%s", rec.Code, rec.Body.String())
	}
}

func TestDiscoverHandler_ServeSync_MissingTransactionID_ReturnsNack400(t *testing.T) {
	h := newTestHandler(t)
	body := `{"context":{"messageId":"m1"},"message":{"intent":{}}}`
	req := httptest.NewRequest(http.MethodGet, "/discover", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.ServeSync(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	var ack beckn.Ack
	if err := json.Unmarshal(rec.Body.Bytes(), &ack); err != nil {
		t.Fatalf("decode nack: %v", err)
	}
	if ack.Status != "NACK" {
		t.Errorf("status = %q, want NACK", ack.Status)
	}
}

// Guards against unbounded-memory-read: a body larger than
// maxDiscoverBodyBytes must be rejected as a bad request, not read fully
// into memory first.
func TestDiscoverHandler_OversizedBody_ReturnsNack400(t *testing.T) {
	h := newTestHandler(t)

	padding := strings.Repeat("x", testMaxBodyBytes+1)
	body := `{"context":{"transactionId":"t","messageId":"m","bapUri":"https://bap"},"message":{"intent":{"textSearch":"` + padding + `"}}}`

	req := httptest.NewRequest(http.MethodPost, "/discover", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for oversized body", rec.Code)
	}
}

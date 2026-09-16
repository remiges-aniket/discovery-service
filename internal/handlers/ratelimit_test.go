package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/remiges-tech/logharbour/logharbour"
)

func ratelimitTestLogger() *logharbour.Logger {
	ctx := logharbour.NewLoggerContext(logharbour.Info)
	return logharbour.NewLoggerWithFallback(ctx, "ratelimit-test", logharbour.NewFallbackWriter(io.Discard, io.Discard))
}

func TestRateLimiter_AllowsRequestsWithinBurst(t *testing.T) {
	rl := NewRateLimiter(1, 3, ratelimitTestLogger())
	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := range 3 {
		req := httptest.NewRequest(http.MethodGet, "/discover", nil)
		req.RemoteAddr = "10.0.0.1:12345"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200 (within burst)", i, rec.Code)
		}
	}
}

func TestRateLimiter_RejectsOverBurstWith429(t *testing.T) {
	rl := NewRateLimiter(1, 2, ratelimitTestLogger())
	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	makeReq := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/discover", nil)
		req.RemoteAddr = "10.0.0.2:12345"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	makeReq() // 1st, within burst
	makeReq() // 2nd, within burst
	rec := makeReq() // 3rd, exceeds burst of 2

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", rec.Code, rec.Body.String())
	}
	var ack struct {
		Status string `json:"status"`
		Error  *struct {
			ErrorCode string `json:"errorCode"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ack); err != nil {
		t.Fatalf("decode nack: %v", err)
	}
	if ack.Status != "NACK" {
		t.Errorf("status field = %q, want NACK", ack.Status)
	}
	if ack.Error == nil || ack.Error.ErrorCode == "" {
		t.Error("expected a populated error on the 429 NACK")
	}
}

func TestRateLimiter_DifferentClientsHaveIndependentBudgets(t *testing.T) {
	rl := NewRateLimiter(1, 1, ratelimitTestLogger())
	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	reqA := httptest.NewRequest(http.MethodGet, "/discover", nil)
	reqA.RemoteAddr = "10.0.0.3:1"
	recA := httptest.NewRecorder()
	handler.ServeHTTP(recA, reqA)
	if recA.Code != http.StatusOK {
		t.Fatalf("client A: status = %d, want 200", recA.Code)
	}

	reqB := httptest.NewRequest(http.MethodGet, "/discover", nil)
	reqB.RemoteAddr = "10.0.0.4:1"
	recB := httptest.NewRecorder()
	handler.ServeHTTP(recB, reqB)
	if recB.Code != http.StatusOK {
		t.Fatalf("client B (different address): status = %d, want 200 (independent budget from A)", recB.Code)
	}
}

func TestClientKey_UsesHostWithoutPort(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/discover", nil)
	req.RemoteAddr = "192.168.1.5:54321"
	if got := clientKey(req); got != "192.168.1.5" {
		t.Errorf("clientKey = %q, want 192.168.1.5", got)
	}
}

func TestClientKey_FallsBackToRawRemoteAddrWhenNotHostPort(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/discover", nil)
	req.RemoteAddr = "not-a-host-port"
	if got := clientKey(req); got != "not-a-host-port" {
		t.Errorf("clientKey = %q, want the raw RemoteAddr unchanged", got)
	}
}

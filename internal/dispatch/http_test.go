package dispatch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/remiges-tushar/discovery-service/internal/beckn"
)

func TestHTTPDispatcher_Deliver_PostsOnDiscoverToBapURIPlusPath(t *testing.T) {
	received := make(chan beckn.OnDiscoverRequest, 1)
	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var body beckn.OnDiscoverRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode callback body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(beckn.Ack{Status: "ACK"})
		received <- body
	}))
	defer srv.Close()

	d := NewHTTPDispatcher(srv.Client(), "/on_discover", nil)
	payload := beckn.OnDiscoverRequest{
		Context: beckn.Context{Action: "on_discover", TransactionID: "t1", MessageID: "m1"},
		Message: beckn.OnDiscoverMessage{Catalogs: []beckn.Catalog{{ID: "c1"}}},
	}

	if err := d.Deliver(context.Background(), srv.URL, payload); err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}

	select {
	case got := <-received:
		if got.Context.TransactionID != "t1" {
			t.Errorf("TransactionID = %q, want t1", got.Context.TransactionID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for callback to be received")
	}

	if gotPath != "/on_discover" {
		t.Errorf("callback path = %q, want /on_discover", gotPath)
	}
}

// The default is spec-correct ("/on_discover" per beckn.yaml, since
// context.bapUri is supposed to already BE the registered subscriber
// base URL — see the Deliver doc comment). But real BAP sandboxes have
// been observed registering an incomplete bapUri (missing their actual
// receiver route prefix, e.g. ONIX's "/bap/receiver"), so the suffix
// must be overridable per deployment until registry-resolved callback
// URLs (D3/M4) make this unnecessary — see CONTEXT.md D14.
func TestHTTPDispatcher_Deliver_UsesConfiguredOnDiscoverPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := NewHTTPDispatcher(srv.Client(), "/bap/receiver/on_discover", nil)
	if err := d.Deliver(context.Background(), srv.URL, beckn.OnDiscoverRequest{}); err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}

	if gotPath != "/bap/receiver/on_discover" {
		t.Errorf("callback path = %q, want /bap/receiver/on_discover", gotPath)
	}
}

type fakeSigner struct {
	sig beckn.Signature
	err error
}

func (f *fakeSigner) Sign(body []byte) (beckn.Signature, error) {
	return f.sig, f.err
}

// Real BAP sandboxes reject callbacks with AUT_SIGNATURE_MISSING when no
// Authorization header is present (see CONTEXT.md D15) — Deliver must
// set one whenever a signer is configured.
func TestHTTPDispatcher_Deliver_SetsAuthorizationHeaderWhenSignerConfigured(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	signer := &fakeSigner{sig: beckn.Signature(`Signature keyId="a|b|ed25519",algorithm="ed25519",created="1",expires="2",headers="h",signature="c2ln"`)}
	d := NewHTTPDispatcher(srv.Client(), "/on_discover", signer)

	if err := d.Deliver(context.Background(), srv.URL, beckn.OnDiscoverRequest{}); err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}
	if gotAuth != string(signer.sig) {
		t.Errorf("Authorization header = %q, want %q", gotAuth, signer.sig)
	}
}

func TestHTTPDispatcher_Deliver_OmitsAuthorizationHeaderWhenNoSignerConfigured(t *testing.T) {
	var gotAuth string
	sawHeader := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, sawHeader = r.Header.Get("Authorization"), r.Header.Get("Authorization") != ""
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := NewHTTPDispatcher(srv.Client(), "/on_discover", nil)
	if err := d.Deliver(context.Background(), srv.URL, beckn.OnDiscoverRequest{}); err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}
	if sawHeader {
		t.Errorf("expected no Authorization header without a signer, got %q", gotAuth)
	}
}

func TestHTTPDispatcher_Deliver_ReturnsErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	d := NewHTTPDispatcher(srv.Client(), "/on_discover", nil)
	err := d.Deliver(context.Background(), srv.URL, beckn.OnDiscoverRequest{})
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
}

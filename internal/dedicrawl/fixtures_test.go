package dedicrawl

import (
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/remiges-tech/logharbour/logharbour"
	"github.com/remiges-tushar/discovery-service/internal/beckn"
)

func testLogger() *logharbour.Logger {
	ctx := logharbour.NewLoggerContext(logharbour.Info)
	return logharbour.NewLoggerWithFallback(ctx, "dedicrawl-test", logharbour.NewFallbackWriter(io.Discard, io.Discard))
}

func mustKeyPair(t *testing.T) (ed25519.PrivateKey, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key pair: %v", err)
	}
	return priv, pub
}

func sampleCatalog(id string) beckn.Catalog {
	return beckn.Catalog{
		ID:         id,
		Descriptor: beckn.Descriptor{Name: "Test Catalog " + id},
		Provider:   beckn.Provider{ID: "provider-1", Descriptor: beckn.Descriptor{Name: "Provider One"}},
		Resources:  []beckn.Resource{{ID: "res-1", Descriptor: beckn.Descriptor{Name: "Resource One"}}},
		Offers:     []beckn.Offer{{ID: "offer-1", Descriptor: beckn.Descriptor{Name: "Offer One"}, ResourceIDs: []string{"res-1"}}},
	}
}

// signedCatalogFile builds a CatalogFile signed by priv and returns both
// the struct and its raw JSON bytes (what a PN would actually serve).
func signedCatalogFile(t *testing.T, priv ed25519.PrivateKey, file CatalogFile) (CatalogFile, []byte) {
	t.Helper()
	file.Signature = ""
	canon, err := CanonicalizeJCS(file)
	if err != nil {
		t.Fatalf("canonicalize catalog file: %v", err)
	}
	file.Signature = signDetached(priv, canon)
	raw, err := json.Marshal(file)
	if err != nil {
		t.Fatalf("marshal catalog file: %v", err)
	}
	return file, raw
}

func signedChangeFile(t *testing.T, priv ed25519.PrivateKey, change CatalogChangeFile) (CatalogChangeFile, []byte) {
	t.Helper()
	change.Signature = ""
	canon, err := CanonicalizeJCS(change)
	if err != nil {
		t.Fatalf("canonicalize change file: %v", err)
	}
	change.Signature = signDetached(priv, canon)
	raw, err := json.Marshal(change)
	if err != nil {
		t.Fatalf("marshal change file: %v", err)
	}
	return change, raw
}

func signedIndexEntry(t *testing.T, priv ed25519.PrivateKey, entry IndexEntry) IndexEntry {
	t.Helper()
	entry.Signature = ""
	canon, err := CanonicalizeJCS(entry)
	if err != nil {
		t.Fatalf("canonicalize index entry: %v", err)
	}
	entry.Signature = signDetached(priv, canon)
	return entry
}

func digestedSubscriberRecord(t *testing.T, record SubscriberRecord) SubscriberRecord {
	t.Helper()
	record.Digest = ""
	canon, err := CanonicalizeJCS(record)
	if err != nil {
		t.Fatalf("canonicalize subscriber record: %v", err)
	}
	record.Digest = Digest(canon)
	return record
}

// fileServer serves a fixed set of URL-path -> raw-bytes pairs over HTTP,
// standing in for a Provider Node's self-hosted static catalog files.
func fileServer(t *testing.T, files map[string][]byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for path, body := range files {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(body)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// fileServerDynamic serves path by calling body() fresh on every request,
// letting a test swap the response between crawls (e.g. to simulate an
// index being updated between two Refresh ticks).
func fileServerDynamic(t *testing.T, path string, body func() []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body())
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

package dedicrawl

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPRegistry_SubscriberRecord_NativeURLArray(t *testing.T) {
	_, pub := mustKeyPair(t)
	pubB64 := base64.StdEncoding.EncodeToString(pub)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/lookup/acme.example.com/dedi/main-store" {
			t.Errorf("unexpected request path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"details": map[string]any{
					"subscriber_id":      "acme.example.com",
					"signing_public_key": pubB64,
					"url":                "https://acme.example.com",
				},
				"meta": map[string]any{
					"catalog_index_urls": []any{
						map[string]any{"url": "https://acme.example.com/index.json"},
					},
				},
			},
		})
	}))
	defer srv.Close()

	registry, err := NewHTTPRegistry(srv.URL, srv.Client())
	if err != nil {
		t.Fatalf("NewHTTPRegistry: %v", err)
	}

	record, pubKey, err := registry.SubscriberRecord(t.Context(), "acme.example.com/dedi/main-store")
	if err != nil {
		t.Fatalf("SubscriberRecord: %v", err)
	}
	if record.SubscriberID != "acme.example.com" {
		t.Errorf("SubscriberID = %q, want acme.example.com", record.SubscriberID)
	}
	if len(record.CatalogIndexURLs) != 1 || record.CatalogIndexURLs[0] != "https://acme.example.com/index.json" {
		t.Errorf("CatalogIndexURLs = %v, want one entry https://acme.example.com/index.json", record.CatalogIndexURLs)
	}
	if string(pubKey) != string(pub) {
		t.Error("returned public key doesn't match the seeded one")
	}
	if err := verifySubscriberRecord(record); err != nil {
		t.Errorf("verifySubscriberRecord failed on HTTPRegistry's self-computed digest: %v", err)
	}
}

func TestHTTPRegistry_SubscriberRecord_DoubleEncodedURLArray(t *testing.T) {
	_, pub := mustKeyPair(t)
	pubB64 := base64.StdEncoding.EncodeToString(pub)

	doubleEncoded := `[{"url": "https://acme.example.com/index.json"}]`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"details": map[string]any{
					"subscriber_id":      "acme.example.com",
					"signing_public_key": pubB64,
				},
				"meta": map[string]any{
					"catalog_index_urls": doubleEncoded,
				},
			},
		})
	}))
	defer srv.Close()

	registry, err := NewHTTPRegistry(srv.URL, srv.Client())
	if err != nil {
		t.Fatalf("NewHTTPRegistry: %v", err)
	}

	record, _, err := registry.SubscriberRecord(t.Context(), "acme.example.com/dedi/main-store")
	if err != nil {
		t.Fatalf("SubscriberRecord: %v", err)
	}
	if len(record.CatalogIndexURLs) != 1 || record.CatalogIndexURLs[0] != "https://acme.example.com/index.json" {
		t.Errorf("CatalogIndexURLs = %v, want the double-encoded array unwrapped", record.CatalogIndexURLs)
	}
}

func TestHTTPRegistry_SubscriberRecord_RejectsMalformedNodeID(t *testing.T) {
	registry, err := NewHTTPRegistry("https://example.com", http.DefaultClient)
	if err != nil {
		t.Fatalf("NewHTTPRegistry: %v", err)
	}

	for _, bad := range []string{"only-one-part", "two/parts", "three//parts", "/leading/slash", "trailing/slash/"} {
		if _, _, err := registry.SubscriberRecord(t.Context(), bad); err == nil {
			t.Errorf("expected an error for malformed subscriberRef %q", bad)
		}
	}
}

func TestHTTPRegistry_SubscriberRecord_NotFoundIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"record not found"}`))
	}))
	defer srv.Close()

	registry, err := NewHTTPRegistry(srv.URL, srv.Client())
	if err != nil {
		t.Fatalf("NewHTTPRegistry: %v", err)
	}

	if _, _, err := registry.SubscriberRecord(t.Context(), "does.not.exist/dedi/nobody"); err == nil {
		t.Fatal("expected an error for a 404 lookup response")
	}
}

func TestHTTPRegistry_SubscriberRecord_MissingSigningKeyIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"details": map[string]any{"subscriber_id": "acme.example.com"},
			},
		})
	}))
	defer srv.Close()

	registry, err := NewHTTPRegistry(srv.URL, srv.Client())
	if err != nil {
		t.Fatalf("NewHTTPRegistry: %v", err)
	}

	if _, _, err := registry.SubscriberRecord(t.Context(), "acme.example.com/dedi/main-store"); err == nil {
		t.Fatal("expected an error when the response has no signing_public_key")
	}
}

func TestHTTPRegistry_Manifest_SelfVerifies(t *testing.T) {
	registry, err := NewHTTPRegistry("https://example.com", http.DefaultClient)
	if err != nil {
		t.Fatalf("NewHTTPRegistry: %v", err)
	}

	manifest, err := registry.Manifest(t.Context(), "https://example.com")
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if _, err := verifyManifest(manifest); err != nil {
		t.Errorf("verifyManifest failed on HTTPRegistry's self-signed manifest: %v", err)
	}
}

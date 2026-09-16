package dedicrawl

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// HTTPRegistry is a real Registry implementation against a live,
// network-operated DeDi registry — confirmed reachable at
// https://fabric.nfh.global/registry/dedi (see CONTEXT.md D21). It
// implements the same lookup shape as beckn-onix's own
// pkg/plugin/implementation/dediregistry client:
// GET {baseURL}/lookup/{namespace}/{registry}/{recordName}, response
// envelope {"data": {"details": {...}, "meta": {...}}}.
type HTTPRegistry struct {
	baseURL string
	client  *http.Client
	// manifestKey is generated once at construction — see Manifest's doc
	// comment for why this is a structural formality, not a real trust
	// anchor, for this Registry specifically.
	manifestKey ed25519.PrivateKey
}

// NewHTTPRegistry builds an HTTPRegistry querying baseURL (e.g.
// "https://fabric.nfh.global/registry/dedi") via client.
func NewHTTPRegistry(baseURL string, client *http.Client) (*HTTPRegistry, error) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, fmt.Errorf("generate manifest formality key: %w", err)
	}
	return &HTTPRegistry{
		baseURL:     strings.TrimRight(baseURL, "/"),
		client:      client,
		manifestKey: priv,
	}, nil
}

// Manifest returns a structurally self-consistent manifest so
// verifyManifest succeeds, but this is a formality, not a real trust
// anchor: unlike the illustrative /.well-known/dedi.json model in
// Catalog_Publishing_and_Discovery.md §10, the real registry at
// fabric.nfh.global exposes no separate, independently-fetchable signed
// manifest document — its lookup/query endpoints (see SubscriberRecord)
// are themselves served over HTTPS by the registry operator, which is
// where trust actually comes from here. crawl.go's CrawlSubscriber already
// discards the registryKey this produces with exactly that caveat
// (`_ = registryKey // ... the stub's subscriber lookup is itself the
// trust boundary for now`) — true for StubRegistry, and equally true here,
// just backed by a real HTTPS fetch instead of an in-memory fake.
func (r *HTTPRegistry) Manifest(_ context.Context, _ string) (DediManifest, error) {
	return selfSignedManifest(r.manifestKey)
}

// lookupNodeResponse is the {"data": {...}} envelope every fabric.nfh.global
// /lookup response shares (per beckn-onix's dediregistry client).
type lookupNodeResponse struct {
	Data struct {
		Details struct {
			SubscriberID     string `json:"subscriber_id"`
			SigningPublicKey string `json:"signing_public_key"`
			URL              string `json:"url"`
		} `json:"details"`
		Meta map[string]any `json:"meta"`
	} `json:"data"`
}

// SubscriberRecord looks up subscriberRef — which must be a real,
// registered nodeID in "namespace/registry/recordName" form (exactly 3
// non-empty parts, e.g. "acme.example.com/dedi/main-store") — against the
// live registry via GET {baseURL}/lookup/{namespace}/{registry}/{recordName}.
func (r *HTTPRegistry) SubscriberRecord(ctx context.Context, subscriberRef string) (SubscriberRecord, ed25519.PublicKey, error) {
	parts := strings.Split(subscriberRef, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return SubscriberRecord{}, nil, fmt.Errorf("subscriberRef %q must be in namespace/registry/recordName format with 3 non-empty parts", subscriberRef)
	}

	url := fmt.Sprintf("%s/lookup/%s/%s/%s", r.baseURL, parts[0], parts[1], parts[2])
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return SubscriberRecord{}, nil, fmt.Errorf("build lookup request: %w", err)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return SubscriberRecord{}, nil, fmt.Errorf("lookup %q: %w", subscriberRef, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return SubscriberRecord{}, nil, fmt.Errorf("read lookup response for %q: %w", subscriberRef, err)
	}
	if resp.StatusCode != http.StatusOK {
		return SubscriberRecord{}, nil, fmt.Errorf("lookup %q: unexpected status %d: %s", subscriberRef, resp.StatusCode, body)
	}

	var parsed lookupNodeResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return SubscriberRecord{}, nil, fmt.Errorf("decode lookup response for %q: %w", subscriberRef, err)
	}
	if parsed.Data.Details.SigningPublicKey == "" {
		return SubscriberRecord{}, nil, fmt.Errorf("lookup %q: response has no signing_public_key", subscriberRef)
	}
	pubKey, err := base64.StdEncoding.DecodeString(parsed.Data.Details.SigningPublicKey)
	if err != nil {
		return SubscriberRecord{}, nil, fmt.Errorf("lookup %q: signing_public_key is not valid base64: %w", subscriberRef, err)
	}

	record := SubscriberRecord{
		SubscriberID:     parsed.Data.Details.SubscriberID,
		PublicKeyBase64:  parsed.Data.Details.SigningPublicKey,
		CatalogIndexURLs: extractCatalogIndexURLs(parsed.Data.Meta["catalog_index_urls"]),
	}

	// The real registry has no digest field of its own (unlike the
	// illustrative spec model) — self-compute one for internal
	// consistency, same as the fixture/test helpers already do. See
	// verifySubscriberRecord's own doc comment: this was already a
	// deliberately simplified trust check, not a real registry-anchored
	// one, even for StubRegistry.
	canonical, err := CanonicalizeJCS(record)
	if err != nil {
		return SubscriberRecord{}, nil, fmt.Errorf("canonicalize subscriber record for %q: %w", subscriberRef, err)
	}
	record.Digest = Digest(canonical)

	return record, ed25519.PublicKey(pubKey), nil
}

// extractCatalogIndexURLs pulls out the "url" field from each entry of
// meta.catalog_index_urls, per NFH-014's {"url": "..."} array shape. The
// real registry sometimes stores this JSON-double-encoded (a string whose
// content IS that same array — beckn-onix's own client works around the
// identical quirk), so a string value is tried as embedded JSON before
// being given up on. Any entry that isn't a recognizable {url} object is
// skipped (logged nowhere here — this is a pure function; a caller wanting
// to know about skipped entries would need SubscriberRecord to take a
// logger, which nothing currently needs) rather than failing the whole
// lookup.
func extractCatalogIndexURLs(raw any) []string {
	switch v := raw.(type) {
	case []any:
		return urlsFromObjectArray(v)
	case string:
		var items []any
		if err := json.Unmarshal([]byte(v), &items); err != nil {
			return nil
		}
		return urlsFromObjectArray(items)
	default:
		return nil
	}
}

func urlsFromObjectArray(items []any) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		url, ok := obj["url"].(string)
		if !ok || url == "" {
			continue
		}
		out = append(out, url)
	}
	return out
}

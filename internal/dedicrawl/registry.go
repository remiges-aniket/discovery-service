package dedicrawl

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
)

// Registry is the port for resolving a Provider Node's Registry-anchored
// identity (§10.2, steps 1-3): the Registry's own manifest (which anchors
// its signing key) and a subscriber's record (which declares where its
// catalog indexes live and its own public key). A real implementation
// would fetch these over HTTPS from a network-operated Registry; no such
// registry is reachable yet, so StubRegistry below is the only
// implementation for now — swapping in an HTTP-backed one later is a
// drop-in adapter change, same pattern as service.OnDiscoverDispatcher.
type Registry interface {
	// Manifest returns the Registry's own signed manifest (as served at
	// /.well-known/dedi.json), used to anchor trust in the Registry's
	// signing key before trusting any subscriber record it vouches for.
	Manifest(ctx context.Context, registryBaseURL string) (DediManifest, error)

	// SubscriberRecord returns the named subscriber's Registry record and
	// its Ed25519 public key (decoded from the record's
	// publicKeyBase64), used to verify the catalogs that subscriber
	// publishes.
	SubscriberRecord(ctx context.Context, subscriberRef string) (SubscriberRecord, ed25519.PublicKey, error)
}

// StubRegistry is an in-memory fake Registry, seeded at construction time
// with known subscriber records and a Registry signing key. It exists
// because no real, reachable DeDi Registry is available to integrate
// against yet; it lets the crawler (and its tests) exercise the full
// §10.2 verify-then-index pipeline end to end against fixture data.
type StubRegistry struct {
	registryKey ed25519.PrivateKey
	records     map[string]stubRecord
}

type stubRecord struct {
	record SubscriberRecord
	pubKey ed25519.PublicKey
}

// NewStubRegistry builds a StubRegistry signing its manifest with
// registryKey. Seed records with Seed before crawling.
func NewStubRegistry(registryKey ed25519.PrivateKey) *StubRegistry {
	return &StubRegistry{
		registryKey: registryKey,
		records:     make(map[string]stubRecord),
	}
}

// Seed registers one subscriber's record and public key, keyed by
// subscriberRef (the same value CrawlSubscriber is called with).
func (r *StubRegistry) Seed(subscriberRef string, record SubscriberRecord, pubKey ed25519.PublicKey) {
	r.records[subscriberRef] = stubRecord{record: record, pubKey: pubKey}
}

// Manifest returns a manifest self-signed by the stub's registry key,
// exposing that key's base64 encoding so callers can verify the
// signature — mirroring a real Registry's manifest being independently
// trustworthy via HTTPS + the Registry's own well-known key.
func (r *StubRegistry) Manifest(_ context.Context, _ string) (DediManifest, error) {
	pub := r.registryKey.Public().(ed25519.PublicKey)
	m := DediManifest{RegistryPublicKeyBase64: base64.StdEncoding.EncodeToString(pub)}

	canonical, err := CanonicalizeJCS(m)
	if err != nil {
		return DediManifest{}, fmt.Errorf("canonicalize manifest: %w", err)
	}
	m.Signature = signDetached(r.registryKey, canonical)
	return m, nil
}

// SubscriberRecord looks up a seeded record. ok=false (via the returned
// error) if subscriberRef was never seeded.
func (r *StubRegistry) SubscriberRecord(_ context.Context, subscriberRef string) (SubscriberRecord, ed25519.PublicKey, error) {
	entry, ok := r.records[subscriberRef]
	if !ok {
		return SubscriberRecord{}, nil, fmt.Errorf("no subscriber record seeded for %q", subscriberRef)
	}
	return entry.record, entry.pubKey, nil
}

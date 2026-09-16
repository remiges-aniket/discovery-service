package dedicrawl

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/remiges-tushar/discovery-service/internal/beckn"
)

// Fixture is the on-disk shape of a dev/demo dedi seed file (see
// SeedFixture): one or more Provider Node subscribers, each publishing a
// fixed set of catalogs. There is no real, reachable DeDi Registry to crawl
// against yet (see CONTEXT.md D18), so this is how "dedi" catalog-source
// mode is exercised end to end — it is a local dev/demo harness, not a
// production integration.
type Fixture struct {
	Subscribers []FixtureSubscriber `json:"subscribers"`
}

// FixtureSubscriber is one Provider Node's declared identity and catalog
// content within a Fixture.
type FixtureSubscriber struct {
	SubscriberRef string           `json:"subscriberRef"`
	Catalogs      []FixtureCatalog `json:"catalogs"`
}

// FixtureCatalog is one baseline-only catalog (no change-file history) a
// fixture subscriber publishes.
type FixtureCatalog struct {
	CatalogID string        `json:"catalogId"`
	Version   string        `json:"version"`
	Catalog   beckn.Catalog `json:"catalog"`
}

// LoadFixture reads and parses a Fixture file from path.
func LoadFixture(path string) (*Fixture, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read fixture %q: %w", path, err)
	}
	var fx Fixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		return nil, fmt.Errorf("decode fixture %q: %w", path, err)
	}
	if len(fx.Subscribers) == 0 {
		return nil, fmt.Errorf("fixture %q declares no subscribers", path)
	}
	return &fx, nil
}

// SeedFixture builds a self-signed, self-contained dedi dataset from fx: a
// fresh Ed25519 dev keypair per role, one self-signed CatalogFile and
// CatalogIndex per subscriber, and a loopback-only HTTP server to serve
// them (dedicrawl.Crawler fetches everything over plain HTTP, so the index
// and baseline files it references must be reachable at real URLs). It
// returns a StubRegistry seeded with every subscriber in fx and the list
// of subscriberRefs seeded (in fx's declared order), plus a cleanup func
// that stops the HTTP server — call it on shutdown.
//
// This is a dev/demo harness only: keys are generated fresh every call
// (nothing outside this process could ever verify them against a real
// registry), matching the same "unverifiable but internally consistent"
// posture as an ephemeral outbound signing key (see cmd/discovery/main.go's
// buildSigner) or StubRegistry's own self-signed manifest.
func SeedFixture(fx *Fixture) (registry *StubRegistry, subscriberRefs []string, cleanup func(), err error) {
	mux := http.NewServeMux()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("listen for fixture HTTP server: %w", err)
	}
	srv := &http.Server{Handler: mux}
	baseURL := "http://" + listener.Addr().String()
	cleanup = func() { _ = srv.Close() }

	_, registryPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		cleanup()
		return nil, nil, nil, fmt.Errorf("generate fixture registry key: %w", err)
	}
	registry = NewStubRegistry(registryPriv)

	for _, sub := range fx.Subscribers {
		subscriberPub, subscriberPriv, err := ed25519.GenerateKey(nil)
		if err != nil {
			cleanup()
			return nil, nil, nil, fmt.Errorf("generate fixture subscriber key for %q: %w", sub.SubscriberRef, err)
		}

		entries := make([]IndexEntry, 0, len(sub.Catalogs))
		for _, cat := range sub.Catalogs {
			file := CatalogFile{CatalogID: cat.CatalogID, Version: cat.Version, Catalog: cat.Catalog}
			canonical, err := CanonicalizeJCS(file)
			if err != nil {
				cleanup()
				return nil, nil, nil, fmt.Errorf("canonicalize fixture catalog %q: %w", cat.CatalogID, err)
			}
			file.Signature = signDetached(subscriberPriv, canonical)
			rawFile, err := json.Marshal(file)
			if err != nil {
				cleanup()
				return nil, nil, nil, fmt.Errorf("marshal fixture catalog %q: %w", cat.CatalogID, err)
			}

			path := fmt.Sprintf("/%s/%s/baseline.json", sub.SubscriberRef, cat.CatalogID)
			mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(rawFile)
			})

			entry := IndexEntry{
				CatalogID:    cat.CatalogID,
				EntryVersion: cat.Version,
				CatalogType:  CatalogTypeRegular,
				IsActive:     true,
				Baseline: FileRef{
					Version: cat.Version,
					URL:     baseURL + path,
					Size:    int64(len(rawFile)),
					Digest:  Digest(rawFile),
				},
			}
			entryCanonical, err := CanonicalizeJCS(entry)
			if err != nil {
				cleanup()
				return nil, nil, nil, fmt.Errorf("canonicalize fixture index entry %q: %w", cat.CatalogID, err)
			}
			entry.Signature = signDetached(subscriberPriv, entryCanonical)
			entries = append(entries, entry)
		}

		rawIndex, err := json.Marshal(CatalogIndex{Entries: entries})
		if err != nil {
			cleanup()
			return nil, nil, nil, fmt.Errorf("marshal fixture index for %q: %w", sub.SubscriberRef, err)
		}
		indexPath := fmt.Sprintf("/%s/index.json", sub.SubscriberRef)
		mux.HandleFunc(indexPath, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(rawIndex)
		})

		record := SubscriberRecord{
			SubscriberID:     sub.SubscriberRef,
			PublicKeyBase64:  base64.StdEncoding.EncodeToString(subscriberPub),
			CatalogIndexURLs: []string{baseURL + indexPath},
		}
		recordCanonical, err := CanonicalizeJCS(record)
		if err != nil {
			cleanup()
			return nil, nil, nil, fmt.Errorf("canonicalize fixture subscriber record %q: %w", sub.SubscriberRef, err)
		}
		record.Digest = Digest(recordCanonical)

		registry.Seed(sub.SubscriberRef, record, subscriberPub)
		subscriberRefs = append(subscriberRefs, sub.SubscriberRef)
	}

	go func() { _ = srv.Serve(listener) }()

	return registry, subscriberRefs, cleanup, nil
}

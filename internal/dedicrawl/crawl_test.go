package dedicrawl

import (
	"encoding/json"
	"testing"
)

// setup builds a full working fixture: a registry with one seeded
// subscriber, one MASTER-less REGULAR catalog served as a signed
// baseline CatalogFile, and a signed CatalogIndex pointing at it. It
// returns the Crawler and the subscriberRef to crawl.
func setup(t *testing.T) (*Crawler, string) {
	t.Helper()
	registryPriv, _ := mustKeyPair(t)
	subscriberPriv, subscriberPub := mustKeyPair(t)

	file, rawFile := signedCatalogFile(t, subscriberPriv, CatalogFile{
		CatalogID: "cat-1",
		Version:   "v1",
		Catalog:   sampleCatalog("cat-1"),
	})

	srv := fileServer(t, map[string][]byte{"/catalog-v1.json": rawFile})

	entry := signedIndexEntry(t, subscriberPriv, IndexEntry{
		CatalogID:    "cat-1",
		EntryVersion: "v1",
		CatalogType:  CatalogTypeRegular,
		IsActive:     true,
		Baseline: FileRef{
			Version: file.Version,
			URL:     srv.URL + "/catalog-v1.json",
			Size:    int64(len(rawFile)),
			Digest:  Digest(rawFile),
		},
	})
	index := CatalogIndex{Entries: []IndexEntry{entry}}
	rawIndex, err := json.Marshal(index)
	if err != nil {
		t.Fatalf("marshal index: %v", err)
	}
	indexSrv := fileServer(t, map[string][]byte{"/index.json": rawIndex})

	record := digestedSubscriberRecord(t, SubscriberRecord{
		SubscriberID:     "pn-1",
		PublicKeyBase64:  "unused-here",
		CatalogIndexURLs: []string{indexSrv.URL + "/index.json"},
	})

	registry := NewStubRegistry(registryPriv)
	registry.Seed("pn-1", record, subscriberPub)

	crawler := NewCrawler(registry, "", srv.Client(), testLogger(), nil, nil, 0.5)
	return crawler, "pn-1"
}

func TestCrawlSubscriber_IndexesAValidCatalog(t *testing.T) {
	crawler, ref := setup(t)

	if err := crawler.CrawlSubscriber(t.Context(), ref); err != nil {
		t.Fatalf("CrawlSubscriber: %v", err)
	}

	got := crawler.cursor.snapshot()
	if len(got) != 1 || got[0].ID != "cat-1" {
		t.Fatalf("snapshot after crawl = %+v, want one catalog cat-1", got)
	}
}

func TestCrawlSubscriber_UnknownSubscriberFails(t *testing.T) {
	crawler, _ := setup(t)
	if err := crawler.CrawlSubscriber(t.Context(), "does-not-exist"); err == nil {
		t.Fatal("expected an error for an unseeded subscriber ref")
	}
}

func TestApplyEntry_DiscardsOnDigestMismatch(t *testing.T) {
	crawler, ref := setup(t)

	// Corrupt the crawler's view by re-seeding an index whose baseline
	// digest doesn't match the served file, then crawl fresh.
	subscriberPriv, subscriberPub := mustKeyPair(t)
	file, rawFile := signedCatalogFile(t, subscriberPriv, CatalogFile{CatalogID: "cat-2", Version: "v1", Catalog: sampleCatalog("cat-2")})
	srv := fileServer(t, map[string][]byte{"/f.json": rawFile})
	entry := signedIndexEntry(t, subscriberPriv, IndexEntry{
		CatalogID: "cat-2", EntryVersion: "v1", CatalogType: CatalogTypeRegular, IsActive: true,
		Baseline: FileRef{Version: file.Version, URL: srv.URL + "/f.json", Size: int64(len(rawFile)), Digest: "BLAKE2b-512=wrong"},
	})
	index := CatalogIndex{Entries: []IndexEntry{entry}}
	rawIndex, _ := json.Marshal(index)
	indexSrv := fileServer(t, map[string][]byte{"/idx.json": rawIndex})
	record := digestedSubscriberRecord(t, SubscriberRecord{SubscriberID: "pn-2", CatalogIndexURLs: []string{indexSrv.URL + "/idx.json"}})

	registryPriv, _ := mustKeyPair(t)
	registry := NewStubRegistry(registryPriv)
	registry.Seed("pn-2", record, subscriberPub)
	badCrawler := NewCrawler(registry, "", srv.Client(), testLogger(), nil, nil, 0.5)

	err := badCrawler.CrawlSubscriber(t.Context(), "pn-2")
	if err == nil {
		t.Fatal("expected crawl to report failure when the only entry has a digest mismatch")
	}
	if got := badCrawler.cursor.snapshot(); len(got) != 0 {
		t.Fatalf("expected nothing indexed after a digest mismatch, got %+v", got)
	}

	// The original, valid fixture must still work independently.
	if err := crawler.CrawlSubscriber(t.Context(), ref); err != nil {
		t.Fatalf("valid crawler should be unaffected: %v", err)
	}
}

func TestApplyEntry_DiscardsOnBadSignature(t *testing.T) {
	registryPriv, _ := mustKeyPair(t)
	subscriberPriv, _ := mustKeyPair(t)
	_, otherPub := mustKeyPair(t)

	file, rawFile := signedCatalogFile(t, subscriberPriv, CatalogFile{CatalogID: "cat-3", Version: "v1", Catalog: sampleCatalog("cat-3")})
	srv := fileServer(t, map[string][]byte{"/f.json": rawFile})
	entry := signedIndexEntry(t, subscriberPriv, IndexEntry{
		CatalogID: "cat-3", EntryVersion: "v1", CatalogType: CatalogTypeRegular, IsActive: true,
		Baseline: FileRef{Version: file.Version, URL: srv.URL + "/f.json", Size: int64(len(rawFile)), Digest: Digest(rawFile)},
	})
	index := CatalogIndex{Entries: []IndexEntry{entry}}
	rawIndex, _ := json.Marshal(index)
	indexSrv := fileServer(t, map[string][]byte{"/idx.json": rawIndex})
	record := digestedSubscriberRecord(t, SubscriberRecord{SubscriberID: "pn-3", CatalogIndexURLs: []string{indexSrv.URL + "/idx.json"}})

	registry := NewStubRegistry(registryPriv)
	// Seed with the WRONG public key so the entry's real signature fails verification.
	registry.Seed("pn-3", record, otherPub)
	crawler := NewCrawler(registry, "", srv.Client(), testLogger(), nil, nil, 0.5)

	err := crawler.CrawlSubscriber(t.Context(), "pn-3")
	if err == nil {
		t.Fatal("expected crawl to report failure when the index entry signature doesn't verify")
	}
	if got := crawler.cursor.snapshot(); len(got) != 0 {
		t.Fatalf("expected nothing indexed after a bad signature, got %+v", got)
	}
}

func TestCrawlSubscriber_VersionUnchangedSkipsRefetch(t *testing.T) {
	crawler, ref := setup(t)
	if err := crawler.CrawlSubscriber(t.Context(), ref); err != nil {
		t.Fatalf("first crawl: %v", err)
	}
	before, _ := crawler.cursor.get("cat-1")

	if err := crawler.CrawlSubscriber(t.Context(), ref); err != nil {
		t.Fatalf("second crawl: %v", err)
	}
	after, _ := crawler.cursor.get("cat-1")

	if before.entryVersion != after.entryVersion {
		t.Fatalf("entryVersion changed on a re-crawl with no version bump: %q -> %q", before.entryVersion, after.entryVersion)
	}
	if got := crawler.cursor.snapshot(); len(got) != 1 {
		t.Fatalf("expected exactly one catalog after re-crawl, got %d", len(got))
	}
}

func TestCrawlSubscriber_RetiredCatalogIsRemovedAndNeverResurrected(t *testing.T) {
	registryPriv, _ := mustKeyPair(t)
	subscriberPriv, subscriberPub := mustKeyPair(t)

	file, rawFile := signedCatalogFile(t, subscriberPriv, CatalogFile{CatalogID: "cat-4", Version: "v1", Catalog: sampleCatalog("cat-4")})
	srv := fileServer(t, map[string][]byte{"/f.json": rawFile})
	baseline := FileRef{Version: file.Version, URL: srv.URL + "/f.json", Size: int64(len(rawFile)), Digest: Digest(rawFile)}

	activeEntry := signedIndexEntry(t, subscriberPriv, IndexEntry{
		CatalogID: "cat-4", EntryVersion: "v1", CatalogType: CatalogTypeRegular, IsActive: true, Baseline: baseline,
	})
	rawIndex1, _ := json.Marshal(CatalogIndex{Entries: []IndexEntry{activeEntry}})

	retiredAt := "2026-09-16T00:00:00Z"
	retiredEntry := signedIndexEntry(t, subscriberPriv, IndexEntry{
		CatalogID: "cat-4", EntryVersion: "v2", CatalogType: CatalogTypeRegular, IsActive: false, Baseline: baseline, RetiredAt: &retiredAt,
	})
	rawIndex2, _ := json.Marshal(CatalogIndex{Entries: []IndexEntry{retiredEntry}})

	// Serve index1 first; we'll swap the handler after the first crawl.
	var currentIndex []byte = rawIndex1
	indexSrv := fileServerDynamic(t, "/idx.json", func() []byte { return currentIndex })

	record := digestedSubscriberRecord(t, SubscriberRecord{SubscriberID: "pn-4", CatalogIndexURLs: []string{indexSrv.URL + "/idx.json"}})
	registry := NewStubRegistry(registryPriv)
	registry.Seed("pn-4", record, subscriberPub)
	crawler := NewCrawler(registry, "", srv.Client(), testLogger(), nil, nil, 0.5)

	if err := crawler.CrawlSubscriber(t.Context(), "pn-4"); err != nil {
		t.Fatalf("first crawl: %v", err)
	}
	if got := crawler.cursor.snapshot(); len(got) != 1 {
		t.Fatalf("expected cat-4 active after first crawl, got %+v", got)
	}

	currentIndex = rawIndex2
	if err := crawler.CrawlSubscriber(t.Context(), "pn-4"); err != nil {
		t.Fatalf("second crawl: %v", err)
	}
	if got := crawler.cursor.snapshot(); len(got) != 0 {
		t.Fatalf("expected cat-4 removed after retirement, got %+v", got)
	}

	// Reverting the index back to "active" must NOT resurrect it.
	currentIndex = rawIndex1
	if err := crawler.CrawlSubscriber(t.Context(), "pn-4"); err != nil {
		t.Fatalf("third crawl: %v", err)
	}
	if got := crawler.cursor.snapshot(); len(got) != 0 {
		t.Fatalf("retired catalog was resurrected: %+v", got)
	}
}

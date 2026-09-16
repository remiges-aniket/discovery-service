package dedicrawl

import (
	"encoding/json"
	"testing"

	"github.com/remiges-tushar/discovery-service/internal/beckn"
)

func TestCrawlSubscriber_AppliesChangeFileChain(t *testing.T) {
	registryPriv, _ := mustKeyPair(t)
	subscriberPriv, subscriberPub := mustKeyPair(t)

	baselineCatalog := sampleCatalog("cat-5")
	file, rawFile := signedCatalogFile(t, subscriberPriv, CatalogFile{CatalogID: "cat-5", Version: "v1", Catalog: baselineCatalog})

	newResource := beckn.Resource{ID: "res-2", Descriptor: beckn.Descriptor{Name: "Resource Two"}}
	change, rawChange := signedChangeFile(t, subscriberPriv, CatalogChangeFile{
		CatalogID:   "cat-5",
		FromVersion: "v1",
		ToVersion:   "v2",
		Resources:   ResourceDelta{Upserts: []beckn.Resource{newResource}, Removals: []string{"res-1"}},
	})

	srv := fileServer(t, map[string][]byte{
		"/baseline.json": rawFile,
		"/change.json":   rawChange,
	})

	baseline := FileRef{Version: file.Version, URL: srv.URL + "/baseline.json", Size: int64(len(rawFile)), Digest: Digest(rawFile)}
	changeRef := ChangeRef{FromVersion: change.FromVersion, ToVersion: change.ToVersion, URL: srv.URL + "/change.json", Size: int64(len(rawChange)), Digest: Digest(rawChange)}

	entryV1 := signedIndexEntry(t, subscriberPriv, IndexEntry{
		CatalogID: "cat-5", EntryVersion: "v1", CatalogType: CatalogTypeRegular, IsActive: true, Baseline: baseline,
	})
	entryV2 := signedIndexEntry(t, subscriberPriv, IndexEntry{
		CatalogID: "cat-5", EntryVersion: "v2", CatalogType: CatalogTypeRegular, IsActive: true,
		Baseline: baseline, Changes: []ChangeRef{changeRef},
	})

	var currentIndex []byte
	rawIndexV1, _ := json.Marshal(CatalogIndex{Entries: []IndexEntry{entryV1}})
	rawIndexV2, _ := json.Marshal(CatalogIndex{Entries: []IndexEntry{entryV2}})
	currentIndex = rawIndexV1
	indexSrv := fileServerDynamic(t, "/idx.json", func() []byte { return currentIndex })

	record := digestedSubscriberRecord(t, SubscriberRecord{SubscriberID: "pn-5", CatalogIndexURLs: []string{indexSrv.URL + "/idx.json"}})
	registry := NewStubRegistry(registryPriv)
	registry.Seed("pn-5", record, subscriberPub)
	// High cutover fraction so a small change file is never treated as
	// bigger than the baseline, keeping this test focused on chain
	// application rather than the cutover rule (covered separately).
	crawler := NewCrawler(registry, "", srv.Client(), testLogger(), nil, nil, 0.99)

	if err := crawler.CrawlSubscriber(t.Context(), "pn-5"); err != nil {
		t.Fatalf("baseline crawl: %v", err)
	}

	currentIndex = rawIndexV2
	if err := crawler.CrawlSubscriber(t.Context(), "pn-5"); err != nil {
		t.Fatalf("change-file crawl: %v", err)
	}

	got := crawler.cursor.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected exactly one catalog, got %+v", got)
	}
	cat := got[0]
	if len(cat.Resources) != 1 || cat.Resources[0].ID != "res-2" {
		t.Fatalf("expected res-1 removed and res-2 upserted, got resources %+v", cat.Resources)
	}
}

func TestCrawlSubscriber_CutoverRuleFetchesBaselineWhenChangesTooLarge(t *testing.T) {
	registryPriv, _ := mustKeyPair(t)
	subscriberPriv, subscriberPub := mustKeyPair(t)

	file, rawFile := signedCatalogFile(t, subscriberPriv, CatalogFile{CatalogID: "cat-6", Version: "v1", Catalog: sampleCatalog("cat-6")})
	// An oversized change file's declared Size will exceed the cutover
	// threshold even though its real byte length is small — Size is a
	// declared index value, not independently re-measured by the crawler.
	_, rawChange := signedChangeFile(t, subscriberPriv, CatalogChangeFile{
		CatalogID: "cat-6", FromVersion: "v1", ToVersion: "v2",
	})

	srv := fileServer(t, map[string][]byte{"/baseline.json": rawFile, "/change.json": rawChange})
	baseline := FileRef{Version: file.Version, URL: srv.URL + "/baseline.json", Size: 10, Digest: Digest(rawFile)}
	changeRef := ChangeRef{FromVersion: "v1", ToVersion: "v2", URL: srv.URL + "/change.json", Size: 100, Digest: Digest(rawChange)}

	entryV1 := signedIndexEntry(t, subscriberPriv, IndexEntry{CatalogID: "cat-6", EntryVersion: "v1", CatalogType: CatalogTypeRegular, IsActive: true, Baseline: baseline})
	entryV2 := signedIndexEntry(t, subscriberPriv, IndexEntry{CatalogID: "cat-6", EntryVersion: "v2", CatalogType: CatalogTypeRegular, IsActive: true, Baseline: baseline, Changes: []ChangeRef{changeRef}})

	var currentIndex []byte
	rawIndexV1, _ := json.Marshal(CatalogIndex{Entries: []IndexEntry{entryV1}})
	rawIndexV2, _ := json.Marshal(CatalogIndex{Entries: []IndexEntry{entryV2}})
	currentIndex = rawIndexV1
	indexSrv := fileServerDynamic(t, "/idx.json", func() []byte { return currentIndex })

	record := digestedSubscriberRecord(t, SubscriberRecord{SubscriberID: "pn-6", CatalogIndexURLs: []string{indexSrv.URL + "/idx.json"}})
	registry := NewStubRegistry(registryPriv)
	registry.Seed("pn-6", record, subscriberPub)
	// cutoverFraction=0.5: change Size (100) far exceeds 0.5*baseline Size (5) -> must fetch baseline.
	crawler := NewCrawler(registry, "", srv.Client(), testLogger(), nil, nil, 0.5)

	if err := crawler.CrawlSubscriber(t.Context(), "pn-6"); err != nil {
		t.Fatalf("baseline crawl: %v", err)
	}
	currentIndex = rawIndexV2
	if err := crawler.CrawlSubscriber(t.Context(), "pn-6"); err != nil {
		t.Fatalf("cutover crawl: %v", err)
	}

	got := crawler.cursor.snapshot()
	if len(got) != 1 || got[0].ID != "cat-6" {
		t.Fatalf("expected cat-6 present via baseline fallback, got %+v", got)
	}
	// The baseline (not the empty-delta change file) must have been used,
	// so the original resource is still present.
	if len(got[0].Resources) != 1 || got[0].Resources[0].ID != "res-1" {
		t.Fatalf("expected baseline content (res-1 present), got resources %+v", got[0].Resources)
	}
}

func TestCrawlIndex_OrdersMasterEntriesBeforeRegular(t *testing.T) {
	var order []string
	entries := []IndexEntry{
		{CatalogID: "regular-1", CatalogType: CatalogTypeRegular},
		{CatalogID: "master-1", CatalogType: CatalogTypeMaster},
		{CatalogID: "regular-2", CatalogType: CatalogTypeRegular},
	}
	for _, e := range orderMastersFirst(entries) {
		order = append(order, e.CatalogID)
	}
	if len(order) != 3 || order[0] != "master-1" {
		t.Fatalf("expected master-1 first, got order %v", order)
	}
}

func TestInScope_FiltersByNetworkAndSchemaType(t *testing.T) {
	c := &Crawler{networkIDs: []string{"net-a"}, schemaTypes: []string{"retail"}}

	inScope := IndexEntry{NetworkIDs: []string{"net-a", "net-b"}, SchemaTypes: []string{"retail"}}
	if !c.inScope(inScope) {
		t.Error("expected entry matching both scope filters to be in scope")
	}

	wrongNetwork := IndexEntry{NetworkIDs: []string{"net-z"}, SchemaTypes: []string{"retail"}}
	if c.inScope(wrongNetwork) {
		t.Error("expected entry with no matching networkId to be out of scope")
	}

	wrongSchema := IndexEntry{NetworkIDs: []string{"net-a"}, SchemaTypes: []string{"logistics"}}
	if c.inScope(wrongSchema) {
		t.Error("expected entry with no matching schemaType to be out of scope")
	}
}

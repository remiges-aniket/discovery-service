package dedicrawl

import (
	"net/http"
	"testing"
)

func TestLoadFixture_ReadsSampleFixture(t *testing.T) {
	fx, err := LoadFixture("testdata/sample-fixture.json")
	if err != nil {
		t.Fatalf("LoadFixture: %v", err)
	}
	if len(fx.Subscribers) != 1 {
		t.Fatalf("expected 1 subscriber, got %d", len(fx.Subscribers))
	}
	sub := fx.Subscribers[0]
	if sub.SubscriberRef != "dedi-demo.local" {
		t.Errorf("SubscriberRef = %q, want dedi-demo.local", sub.SubscriberRef)
	}
	if len(sub.Catalogs) != 1 || sub.Catalogs[0].CatalogID != "dedi-demo.local/sample-catalog" {
		t.Fatalf("unexpected catalogs: %+v", sub.Catalogs)
	}
}

func TestLoadFixture_ErrorsOnMissingFile(t *testing.T) {
	if _, err := LoadFixture("testdata/does-not-exist.json"); err == nil {
		t.Fatal("expected an error for a missing fixture file")
	}
}

func TestSeedFixture_CrawlableEndToEnd(t *testing.T) {
	fx, err := LoadFixture("testdata/sample-fixture.json")
	if err != nil {
		t.Fatalf("LoadFixture: %v", err)
	}

	registry, refs, cleanup, err := SeedFixture(fx)
	if err != nil {
		t.Fatalf("SeedFixture: %v", err)
	}
	t.Cleanup(cleanup)

	if len(refs) != 1 || refs[0] != "dedi-demo.local" {
		t.Fatalf("subscriberRefs = %v, want [dedi-demo.local]", refs)
	}

	crawler := NewCrawler(registry, "", &http.Client{}, testLogger(), nil, nil, 0.5)
	for _, ref := range refs {
		if err := crawler.CrawlSubscriber(t.Context(), ref); err != nil {
			t.Fatalf("CrawlSubscriber(%q): %v", ref, err)
		}
	}

	got := crawler.cursor.snapshot()
	if len(got) != 1 || got[0].ID != "dedi-demo.local/sample-catalog" {
		t.Fatalf("snapshot after crawling the fixture = %+v, want the fixture's one sample catalog", got)
	}
}

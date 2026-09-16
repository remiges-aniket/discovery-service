package service

import (
	"testing"

	"github.com/remiges-tushar/discovery-service/internal/beckn"
)

func TestBuildFixedOnDiscover_EchoesContextWithOnDiscoverAction(t *testing.T) {
	reqCtx := beckn.Context{
		Domain:        "retail",
		Action:        "discover",
		Version:       "2.0.0",
		BapID:         "bap.example.com",
		BapURI:        "https://bap.example.com",
		TransactionID: "txn-1",
		MessageID:     "msg-1",
	}

	resp := BuildFixedOnDiscover(reqCtx, "our-own-bpp.example.com", "https://our-own-bpp.example.com")

	if resp.Context.Action != "on_discover" {
		t.Fatalf("Context.Action = %q, want %q", resp.Context.Action, "on_discover")
	}
	if resp.Context.TransactionID != reqCtx.TransactionID {
		t.Fatalf("TransactionID = %q, want %q", resp.Context.TransactionID, reqCtx.TransactionID)
	}
	if resp.Context.MessageID != reqCtx.MessageID {
		t.Fatalf("MessageID = %q, want %q", resp.Context.MessageID, reqCtx.MessageID)
	}
	if resp.Context.BapID != reqCtx.BapID || resp.Context.BapURI != reqCtx.BapURI {
		t.Fatalf("BapID/BapURI not echoed: got %+v", resp.Context)
	}
}

func TestBuildFixedOnDiscover_FillsOwnBppIdentityWhenRequestOmitsIt(t *testing.T) {
	// Mirrors a real observed request: the BAP's /discover call carries no
	// bppId/bppUri at all (it's calling a network-wide discover service,
	// not a specific BPP) — the responding service must stamp its OWN
	// identity onto the on_discover context rather than leaving it blank.
	reqCtx := beckn.Context{TransactionID: "t", MessageID: "m", BapID: "bap.example.com"}

	resp := BuildFixedOnDiscover(reqCtx, "ds-fabric.ion.id", "https://discover.infra.ion.id")

	if resp.Context.BppID != "ds-fabric.ion.id" {
		t.Errorf("Context.BppID = %q, want the configured own identity ds-fabric.ion.id", resp.Context.BppID)
	}
	if resp.Context.BppURI != "https://discover.infra.ion.id" {
		t.Errorf("Context.BppURI = %q, want https://discover.infra.ion.id", resp.Context.BppURI)
	}
}

func TestBuildFixedOnDiscover_DoesNotOverrideBppIdentityWhenRequestAlreadyHasOne(t *testing.T) {
	// If the BAP specifically targeted a BPP (context.bppId/bppUri already
	// set in the request), that MUST be echoed back unchanged — it is not
	// our identity to overwrite.
	reqCtx := beckn.Context{TransactionID: "t", MessageID: "m", BppID: "some-other-bpp.example.com", BppURI: "https://some-other-bpp.example.com"}

	resp := BuildFixedOnDiscover(reqCtx, "ds-fabric.ion.id", "https://discover.infra.ion.id")

	if resp.Context.BppID != "some-other-bpp.example.com" {
		t.Errorf("Context.BppID = %q, want the request's own bppId to be preserved", resp.Context.BppID)
	}
	if resp.Context.BppURI != "https://some-other-bpp.example.com" {
		t.Errorf("Context.BppURI = %q, want the request's own bppUri to be preserved", resp.Context.BppURI)
	}
}

func TestBuildFixedOnDiscover_ReturnsAtLeastOneSpecShapedCatalog(t *testing.T) {
	resp := BuildFixedOnDiscover(beckn.Context{TransactionID: "t", MessageID: "m"}, "own-bpp.example.com", "https://own-bpp.example.com")

	if len(resp.Message.Catalogs) == 0 {
		t.Fatal("expected at least one catalog in the fixed response")
	}

	cat := resp.Message.Catalogs[0]
	if cat.ID == "" {
		t.Error("catalog.id must not be empty")
	}
	if cat.Descriptor.Name == "" {
		t.Error("catalog.descriptor.name must not be empty")
	}
	if cat.Provider.ID == "" {
		t.Error("catalog.provider.id must not be empty")
	}
	// Catalog schema requires anyOf [resources, offers] be present.
	if len(cat.Resources) == 0 && len(cat.Offers) == 0 {
		t.Error("catalog must have at least one of resources/offers")
	}
}

func TestBuildFixedOnDiscover_EveryOfferReferencesAResourceInTheSameCatalog(t *testing.T) {
	resp := BuildFixedOnDiscover(beckn.Context{TransactionID: "t", MessageID: "m"}, "own-bpp.example.com", "https://own-bpp.example.com")

	if len(resp.Message.Catalogs) == 0 {
		t.Fatal("expected at least one catalog")
	}

	for _, cat := range resp.Message.Catalogs {
		resourceIDs := make(map[string]bool, len(cat.Resources))
		for _, r := range cat.Resources {
			if r.ID == "" {
				t.Errorf("catalog %q: resource.id must not be empty", cat.ID)
			}
			if r.Descriptor.Name == "" {
				t.Errorf("catalog %q, resource %q: descriptor.name must not be empty", cat.ID, r.ID)
			}
			resourceIDs[r.ID] = true
		}
		for _, o := range cat.Offers {
			if len(o.ResourceIDs) == 0 {
				t.Errorf("catalog %q, offer %q: resourceIds must not be empty", cat.ID, o.ID)
			}
			for _, rid := range o.ResourceIDs {
				if !resourceIDs[rid] {
					t.Errorf("catalog %q, offer %q references unknown resourceId %q", cat.ID, o.ID, rid)
				}
			}
		}
	}
}

func TestBuildFixedOnDiscover_PerCatalogBppIdentityIsPreservedWhenPresentInData(t *testing.T) {
	// data/catalog.json intentionally models a real observed shape: one
	// catalog omits its own bppId/bppUri (should inherit the response
	// context's identity), another sets its own (a fan-out/federated
	// result from a different BPP) which must NOT be clobbered.
	resp := BuildFixedOnDiscover(beckn.Context{TransactionID: "t", MessageID: "m"}, "ds-fabric.ion.id", "https://discover.infra.ion.id")

	var sawInherited, sawOwnIdentity bool
	for _, cat := range resp.Message.Catalogs {
		switch cat.BppID {
		case "ds-fabric.ion.id":
			sawInherited = true
		case "":
			t.Errorf("catalog %q: bppId must never be empty in the response", cat.ID)
		default:
			sawOwnIdentity = true
		}
	}
	if !sawInherited {
		t.Error("expected at least one catalog to inherit the response's own bppId")
	}
	if !sawOwnIdentity {
		t.Error("expected at least one catalog to keep its own distinct bppId from data/catalog.json")
	}
}

func TestBuildFixedOnDiscover_DoesNotMutateSharedCatalogDataAcrossCalls(t *testing.T) {
	// BuildFixedOnDiscover is called concurrently by many /discover
	// requests; if it handed out the same underlying Catalog value (or
	// mutated shared state) instead of a per-call copy, one request's
	// context could leak into another's response.
	first := BuildFixedOnDiscover(beckn.Context{TransactionID: "t1", MessageID: "m1"}, "bpp-one.example.com", "https://bpp-one.example.com")
	second := BuildFixedOnDiscover(beckn.Context{TransactionID: "t2", MessageID: "m2"}, "bpp-two.example.com", "https://bpp-two.example.com")

	// Compare only catalogs that had no bppId of their own in the source
	// data (those inherit from the call's own identity and so must differ
	// between the two calls); catalogs with their own bppId in the data
	// are identical across calls by design and are not a useful signal
	// here.
	inheritedFirst := inheritedCatalogBppIDs(first.Message.Catalogs, "bpp-one.example.com")
	inheritedSecond := inheritedCatalogBppIDs(second.Message.Catalogs, "bpp-two.example.com")
	if len(inheritedFirst) == 0 || len(inheritedSecond) == 0 {
		t.Fatal("expected at least one catalog per call to inherit the call's own bppId")
	}
}

func TestBuildOnDiscover_UsesGivenCatalogsInstedOfTheEmbeddedDemoData(t *testing.T) {
	external := []beckn.Catalog{{
		ID:         "external-catalog-1",
		Descriptor: beckn.Descriptor{Name: "External Catalog"},
		Provider:   beckn.Provider{ID: "ext-provider", Descriptor: beckn.Descriptor{Name: "External Provider"}},
		Resources:  []beckn.Resource{{ID: "ext-res-1", Descriptor: beckn.Descriptor{Name: "External Widget"}}},
	}}

	resp := BuildOnDiscover(beckn.Context{TransactionID: "t", MessageID: "m"}, "own-bpp.example.com", "https://own-bpp.example.com", external)

	if len(resp.Message.Catalogs) != 1 || resp.Message.Catalogs[0].ID != "external-catalog-1" {
		t.Fatalf("expected the given external catalogs to be used, got %+v", resp.Message.Catalogs)
	}
}

func TestBuildOnDiscover_StampsOwnIdentityOnGivenCatalogsThatOmitOne(t *testing.T) {
	external := []beckn.Catalog{
		{ID: "cat-no-identity", Descriptor: beckn.Descriptor{Name: "A"}, Provider: beckn.Provider{ID: "p"}},
		{ID: "cat-own-identity", BppID: "other-bpp.example.com", BppURI: "https://other-bpp.example.com", Descriptor: beckn.Descriptor{Name: "B"}, Provider: beckn.Provider{ID: "p"}},
	}

	resp := BuildOnDiscover(beckn.Context{TransactionID: "t", MessageID: "m"}, "own-bpp.example.com", "https://own-bpp.example.com", external)

	byID := map[string]beckn.Catalog{}
	for _, c := range resp.Message.Catalogs {
		byID[c.ID] = c
	}
	if byID["cat-no-identity"].BppID != "own-bpp.example.com" {
		t.Errorf("catalog with no bppId should inherit the response's own identity, got %q", byID["cat-no-identity"].BppID)
	}
	if byID["cat-own-identity"].BppID != "other-bpp.example.com" {
		t.Errorf("catalog with its own bppId must not be overwritten, got %q", byID["cat-own-identity"].BppID)
	}
}

func TestEmbeddedCatalogStore_CatalogsMatchesTheFixedDemoCatalog(t *testing.T) {
	store := EmbeddedCatalogStore{}
	got := store.Catalogs()

	if len(got) == 0 || len(got) != len(fixedCatalogs) {
		t.Fatalf("EmbeddedCatalogStore.Catalogs() returned %d catalogs, want %d matching the embedded demo data", len(got), len(fixedCatalogs))
	}
}

func TestEmbeddedCatalogStore_CatalogsReturnsACopyNotSharedState(t *testing.T) {
	store := EmbeddedCatalogStore{}
	first := store.Catalogs()
	originalID := first[0].ID
	first[0].ID = "mutated"

	second := store.Catalogs()
	if second[0].ID != originalID {
		t.Fatalf("mutating a returned slice affected the shared embedded catalog data: %+v", second)
	}
}

func inheritedCatalogBppIDs(catalogs []beckn.Catalog, wantBppID string) []string {
	var ids []string
	for _, c := range catalogs {
		if c.BppID == wantBppID {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

package service

import (
	"context"
	"testing"

	"github.com/remiges-tushar/discovery-service/internal/beckn"
)

func sampleMatchCatalog() beckn.Catalog {
	return beckn.Catalog{
		ID:         "cat-1",
		Descriptor: beckn.Descriptor{Name: "Test Catalog"},
		Provider: beckn.Provider{
			ID:         "provider-1",
			Descriptor: beckn.Descriptor{Name: "Test Provider"},
			AvailableAt: []beckn.Location{
				{Geo: beckn.GeoJSONGeometry{Type: "Point", Coordinates: []any{77.5946, 12.9716}}}, // Bengaluru
			},
		},
		Resources: []beckn.Resource{
			{ID: "res-laptop", Descriptor: beckn.Descriptor{Name: "Premium Gaming Laptop", ShortDesc: "RTX graphics"}, ResourceAttributes: map[string]any{"rating": map[string]any{"value": 4.5}}},
			{ID: "res-mouse", Descriptor: beckn.Descriptor{Name: "Wireless Mouse", ShortDesc: "Ergonomic"}, ResourceAttributes: map[string]any{"rating": map[string]any{"value": 2.0}}},
		},
		Offers: []beckn.Offer{
			{ID: "offer-laptop", Descriptor: beckn.Descriptor{Name: "10% off"}, ResourceIDs: []string{"res-laptop"}},
			{ID: "offer-mouse", Descriptor: beckn.Descriptor{Name: "5% off"}, ResourceIDs: []string{"res-mouse"}},
			{ID: "offer-storewide", Descriptor: beckn.Descriptor{Name: "Storewide sale"}},
		},
	}
}

// sampleMatchCatalogNoStorewideOffer is sampleMatchCatalog without a
// catalog-wide (no ResourceIDs) offer — used by tests asserting a catalog
// is dropped entirely when its resources are fully filtered out, since a
// catalog-wide offer legitimately keeps a catalog alive regardless of
// resource matches (see filterOffersByResources).
func sampleMatchCatalogNoStorewideOffer() beckn.Catalog {
	c := sampleMatchCatalog()
	c.Offers = c.Offers[:2] // drop offer-storewide
	return c
}

func TestMatchCatalogs_EmptyIntentReturnsEverythingUnchanged(t *testing.T) {
	catalogs := []beckn.Catalog{sampleMatchCatalog()}
	got := matchCatalogs(t.Context(), catalogs, beckn.Intent{})
	if len(got) != 1 || len(got[0].Resources) != 2 || len(got[0].Offers) != 3 {
		t.Fatalf("expected catalogs unchanged for empty intent, got %+v", got)
	}
}

func TestMatchCatalogs_TextSearchNarrowsResourcesAndOffers(t *testing.T) {
	catalogs := []beckn.Catalog{sampleMatchCatalog()}
	got := matchCatalogs(t.Context(), catalogs, beckn.Intent{TextSearch: "laptop"})

	if len(got) != 1 {
		t.Fatalf("expected 1 catalog to survive, got %d", len(got))
	}
	if len(got[0].Resources) != 1 || got[0].Resources[0].ID != "res-laptop" {
		t.Fatalf("expected only res-laptop to survive, got %+v", got[0].Resources)
	}
	offerIDs := map[string]bool{}
	for _, o := range got[0].Offers {
		offerIDs[o.ID] = true
	}
	if !offerIDs["offer-laptop"] || !offerIDs["offer-storewide"] || offerIDs["offer-mouse"] {
		t.Fatalf("expected offer-laptop + offer-storewide to survive, offer-mouse to be dropped, got %+v", got[0].Offers)
	}
}

func TestMatchCatalogs_TextSearchIsCaseInsensitiveAndRequiresAllTerms(t *testing.T) {
	catalogs := []beckn.Catalog{sampleMatchCatalog()}
	got := matchCatalogs(t.Context(), catalogs, beckn.Intent{TextSearch: "GAMING laptop"})
	if len(got) != 1 || len(got[0].Resources) != 1 || got[0].Resources[0].ID != "res-laptop" {
		t.Fatalf("expected case-insensitive multi-term match on res-laptop, got %+v", got)
	}

	none := matchCatalogs(t.Context(), []beckn.Catalog{sampleMatchCatalogNoStorewideOffer()}, beckn.Intent{TextSearch: "laptop nonexistentterm"})
	if len(none) != 0 {
		t.Fatalf("expected zero catalogs when not all terms match, got %+v", none)
	}
}

func TestMatchCatalogs_TextSearchMatchingNothingDropsTheCatalog(t *testing.T) {
	catalogs := []beckn.Catalog{sampleMatchCatalogNoStorewideOffer()}
	got := matchCatalogs(t.Context(), catalogs, beckn.Intent{TextSearch: "doesnotexist12345"})
	if len(got) != 0 {
		t.Fatalf("expected the catalog to be dropped entirely, got %+v", got)
	}
}

func TestMatchCatalogs_JSONPathFilterNarrowsResources(t *testing.T) {
	catalogs := []beckn.Catalog{sampleMatchCatalog()}
	got := matchCatalogs(t.Context(), catalogs, beckn.Intent{
		Filters: &beckn.Filters{Type: "jsonpath", Expression: `$[?(@.resourceAttributes.rating.value >= 4.0)]`},
	})
	if len(got) != 1 || len(got[0].Resources) != 1 || got[0].Resources[0].ID != "res-laptop" {
		t.Fatalf("expected only res-laptop (rating 4.5) to survive the filter, got %+v", got)
	}
}

func TestMatchCatalogs_TextSearchAndJSONPathFilterCombineAsAnd(t *testing.T) {
	catalogs := []beckn.Catalog{sampleMatchCatalogNoStorewideOffer()}
	// textSearch matches only res-mouse; the filter matches only res-laptop
	// (rating >= 4.0) — combined, nothing survives.
	got := matchCatalogs(t.Context(), catalogs, beckn.Intent{
		TextSearch: "mouse",
		Filters:    &beckn.Filters{Type: "jsonpath", Expression: `$[?(@.resourceAttributes.rating.value >= 4.0)]`},
	})
	if len(got) != 0 {
		t.Fatalf("expected combined textSearch+filters to leave nothing, got %+v", got)
	}
}

func TestMatchCatalogs_SpatialDWithinAnyMatchesWithinDistance(t *testing.T) {
	catalogs := []beckn.Catalog{sampleMatchCatalog()}
	dist := 2000.0 // meters — well within Bengaluru city bounds of the sample point

	got := matchCatalogs(t.Context(), catalogs, beckn.Intent{
		Spatial: []beckn.SpatialConstraint{{
			Op:             "S_DWITHIN",
			Geometry:       &beckn.GeoJSONGeometry{Type: "Point", Coordinates: []any{77.5946, 12.9716}},
			DistanceMeters: &dist,
			Quantifier:     "ANY",
		}},
	})
	if len(got) != 1 {
		t.Fatalf("expected the catalog to survive a spatial constraint matching its own location, got %+v", got)
	}
}

func TestMatchCatalogs_SpatialDWithinExcludesOutOfRange(t *testing.T) {
	catalogs := []beckn.Catalog{sampleMatchCatalog()}
	dist := 1000.0 // meters

	got := matchCatalogs(t.Context(), catalogs, beckn.Intent{
		Spatial: []beckn.SpatialConstraint{{
			Op:             "S_DWITHIN",
			Geometry:       &beckn.GeoJSONGeometry{Type: "Point", Coordinates: []any{0.0, 0.0}}, // far away
			DistanceMeters: &dist,
			Quantifier:     "ANY",
		}},
	})
	if len(got) != 0 {
		t.Fatalf("expected the catalog to be excluded by a far-away spatial constraint, got %+v", got)
	}
}

func TestMatchCatalogs_UnsupportedSpatialOpIsPermissive(t *testing.T) {
	catalogs := []beckn.Catalog{sampleMatchCatalog()}
	dist := 1.0

	got := matchCatalogs(t.Context(), catalogs, beckn.Intent{
		Spatial: []beckn.SpatialConstraint{{
			Op:             "S_INTERSECTS", // not implemented — must not exclude everything
			Geometry:       &beckn.GeoJSONGeometry{Type: "Polygon"},
			DistanceMeters: &dist,
		}},
	})
	if len(got) != 1 {
		t.Fatalf("expected an unsupported spatial op to be permissive (catalog kept), got %+v", got)
	}
}

func TestMatchCatalogs_StopsEarlyWhenContextAlreadyExpired(t *testing.T) {
	catalogs := []beckn.Catalog{sampleMatchCatalog(), sampleMatchCatalog()}
	ctx, cancel := context.WithTimeout(t.Context(), 0) // already expired
	defer cancel()
	<-ctx.Done()

	got := matchCatalogs(ctx, catalogs, beckn.Intent{TextSearch: "laptop"})
	if len(got) != 0 {
		t.Fatalf("expected an already-expired context to stop matching before anything is processed, got %d catalogs", len(got))
	}
}

func TestMatchCatalogs_MediaSearchIsANoOp(t *testing.T) {
	catalogs := []beckn.Catalog{sampleMatchCatalog()}
	got := matchCatalogs(t.Context(), catalogs, beckn.Intent{MediaSearch: &beckn.MediaSearch{Media: []beckn.MediaInput{{Type: "image", URL: "https://example.com/x.png"}}}})
	if len(got) != 1 || len(got[0].Resources) != 2 {
		t.Fatalf("expected mediaSearch to be a no-op (nothing filtered), got %+v", got)
	}
}

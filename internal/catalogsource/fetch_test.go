package catalogsource

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/remiges-tech/logharbour/logharbour"
)

func testLogger() *logharbour.Logger {
	ctx := logharbour.NewLoggerContext(logharbour.Info)
	return logharbour.NewLoggerWithFallback(ctx, "catalogsource-test", logharbour.NewFallbackWriter(io.Discard, io.Discard))
}

// newBPPStub serves a single-page catalog list plus per-catalog detail
// responses, mirroring bpp-application's dashboard API shape exactly
// (internal/dashboardsvc/catalog_handlers.go: HandleListCatalogs,
// HandleGetCatalog).
func newBPPStub(t *testing.T, ids []string, detail func(id string) catalogDetailResponse) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/catalogs", func(w http.ResponseWriter, r *http.Request) {
		items := make([]catalogListItem, len(ids))
		for i, id := range ids {
			items[i] = catalogListItem{ID: id}
		}
		_ = json.NewEncoder(w).Encode(catalogListResponse{Items: items, Total: len(ids), Page: 1, Limit: listPageSize})
	})
	for _, id := range ids {
		mux.HandleFunc(fmt.Sprintf("/api/v1/catalogs/%s", id), func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(detail(id))
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func sampleDetail(id string) catalogDetailResponse {
	return catalogDetailResponse{
		Catalog: catalogDetail{ID: id, Name: "Catalog " + id, ProviderID: "prov-1", ProviderName: "Provider One"},
		Resources: []resourceItem{{
			ID:                 "res-1",
			Name:               "Widget",
			MediaFiles:         json.RawMessage(`[{"uri":"https://example.com/w.png","mimeType":"image/png"}]`),
			ResourceAttributes: json.RawMessage(`{"@context":"https://schema.ion.id/context/retail.jsonld","@type":"Product","brand":"Acme"}`),
		}},
		Offers: []offerItem{{
			ID:                      "offer-1",
			Name:                    "10% off",
			ResourceIDs:             []string{"res-1"},
			ConsiderationAttributes: json.RawMessage(`{"@context":"https://schema.ion.id/context/retail.jsonld","@type":"PriceSpecification","price":"499.00"}`),
		}},
	}
}

func TestHTTPSource_FetchAll_MapsBPPResponseToBecknCatalog(t *testing.T) {
	srv := newBPPStub(t, []string{"cat-1"}, sampleDetail)

	src := NewHTTPSource([]string{srv.URL}, srv.Client(), testLogger())
	catalogs, anySuccess := src.FetchAll(t.Context())

	if !anySuccess {
		t.Fatal("expected anySuccess=true")
	}
	if len(catalogs) != 1 {
		t.Fatalf("got %d catalogs, want 1", len(catalogs))
	}
	cat := catalogs[0]
	if cat.ID != "cat-1" || cat.Descriptor.Name != "Catalog cat-1" {
		t.Errorf("catalog mapped incorrectly: %+v", cat)
	}
	if cat.Provider.ID != "prov-1" || cat.Provider.Descriptor.Name != "Provider One" {
		t.Errorf("provider mapped incorrectly: %+v", cat.Provider)
	}
	if len(cat.Resources) != 1 {
		t.Fatalf("got %d resources, want 1", len(cat.Resources))
	}
	res := cat.Resources[0]
	if res.ID != "res-1" || res.Descriptor.Name != "Widget" {
		t.Errorf("resource mapped incorrectly: %+v", res)
	}
	if len(res.Descriptor.MediaFile) != 1 || res.Descriptor.MediaFile[0].URI != "https://example.com/w.png" {
		t.Errorf("mediaFiles mapped incorrectly: %+v", res.Descriptor.MediaFile)
	}
	if res.ResourceAttributes["brand"] != "Acme" {
		t.Errorf("resourceAttributes mapped incorrectly: %+v", res.ResourceAttributes)
	}
	if len(cat.Offers) != 1 {
		t.Fatalf("got %d offers, want 1", len(cat.Offers))
	}
	offer := cat.Offers[0]
	if len(offer.ResourceIDs) != 1 || offer.ResourceIDs[0] != "res-1" {
		t.Errorf("offer.resourceIds mapped incorrectly: %+v", offer.ResourceIDs)
	}
	if len(offer.Considerations) != 1 || offer.Considerations[0].ConsiderationAttributes["price"] != "499.00" {
		t.Errorf("offer.considerations mapped incorrectly: %+v", offer.Considerations)
	}
}

func TestHTTPSource_FetchAll_PagesThroughFullCatalogList(t *testing.T) {
	// listPageSize is 100; 101 catalogs forces a second page.
	ids := make([]string, 101)
	for i := range ids {
		ids[i] = fmt.Sprintf("cat-%d", i)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/catalogs", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		var pageIDs []string
		switch page {
		case "1":
			pageIDs = ids[:listPageSize]
		case "2":
			pageIDs = ids[listPageSize:]
		}
		items := make([]catalogListItem, len(pageIDs))
		for i, id := range pageIDs {
			items[i] = catalogListItem{ID: id}
		}
		_ = json.NewEncoder(w).Encode(catalogListResponse{Items: items, Total: len(ids), Page: 1, Limit: listPageSize})
	})
	for _, id := range ids {
		mux.HandleFunc(fmt.Sprintf("/api/v1/catalogs/%s", id), func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(sampleDetail(id))
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	src := NewHTTPSource([]string{srv.URL}, srv.Client(), testLogger())
	catalogs, anySuccess := src.FetchAll(t.Context())

	if !anySuccess {
		t.Fatal("expected anySuccess=true")
	}
	if len(catalogs) != len(ids) {
		t.Fatalf("got %d catalogs, want %d (pagination did not fetch the second page)", len(catalogs), len(ids))
	}
}

func TestHTTPSource_FetchAll_OneSourceFailingDoesNotBlockOthers(t *testing.T) {
	good := newBPPStub(t, []string{"cat-1"}, sampleDetail)
	unreachable := "http://127.0.0.1:1" // nothing listens here

	src := NewHTTPSource([]string{unreachable, good.URL}, good.Client(), testLogger())
	catalogs, anySuccess := src.FetchAll(t.Context())

	if !anySuccess {
		t.Fatal("expected anySuccess=true since one of the two sources succeeded")
	}
	if len(catalogs) != 1 {
		t.Fatalf("got %d catalogs, want 1 from the reachable source", len(catalogs))
	}
}

func TestHTTPSource_FetchAll_AllSourcesFailingReturnsNoSuccess(t *testing.T) {
	src := NewHTTPSource([]string{"http://127.0.0.1:1"}, &http.Client{Timeout: time.Second}, testLogger())
	_, anySuccess := src.FetchAll(t.Context())

	if anySuccess {
		t.Fatal("expected anySuccess=false when every source fails")
	}
}

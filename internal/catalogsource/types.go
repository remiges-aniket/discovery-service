// Package catalogsource is the CONTEXT.md D17 adapter that polls one or
// more BPP instances' existing dashboard/admin HTTP API
// (GET /api/v1/catalogs, GET /api/v1/catalogs/{id} — see
// bpp-application's internal/dashboardsvc) and maps their responses into
// beckn.Catalog, so discovery-service can serve real product data instead
// of the embedded M1 demo catalog. It implements service.CatalogStore.
//
// This is a deliberate stopgap, not a stable integration: those endpoints
// are an internal dashboard API, not a published Beckn contract, and
// carry no authentication or versioning guarantee. It exists because
// bpp-application has no other network-reachable way to expose its
// catalog data without code changes on its side (see CONTEXT.md D17 for
// the full reasoning — a filesystem-path-based "crawl" was ruled out
// because it cannot survive separate Cloud Run deployments, which have no
// shared filesystem between services).
package catalogsource

import "encoding/json"

// catalogListResponse is GET {baseURL}/api/v1/catalogs.
type catalogListResponse struct {
	Items []catalogListItem `json:"items"`
	Total int               `json:"total"`
	Page  int               `json:"page"`
	Limit int               `json:"limit"`
}

type catalogListItem struct {
	ID string `json:"id"`
}

// catalogDetailResponse is GET {baseURL}/api/v1/catalogs/{id}.
type catalogDetailResponse struct {
	Catalog   catalogDetail  `json:"catalog"`
	Resources []resourceItem `json:"resources"`
	Offers    []offerItem    `json:"offers"`
}

type catalogDetail struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	ShortDesc     string  `json:"shortDesc"`
	ProviderID    string  `json:"providerId"`
	ProviderName  string  `json:"providerName"`
	CatalogType   *string `json:"catalogType"`
	ValidityStart *string `json:"validityStart"`
	ValidityEnd   *string `json:"validityEnd"`
}

type resourceItem struct {
	ID                 string          `json:"id"`
	Name               string          `json:"name"`
	ShortDesc          string          `json:"shortDesc"`
	MediaFiles         json.RawMessage `json:"mediaFiles"`
	ResourceAttributes json.RawMessage `json:"resourceAttributes"`
}

type offerItem struct {
	ID                      string          `json:"id"`
	Name                    string          `json:"name"`
	ShortDesc               string          `json:"shortDesc"`
	ResourceIDs             []string        `json:"resourceIds"`
	ValidityStart           *string         `json:"validityStart"`
	ValidityEnd             *string         `json:"validityEnd"`
	ConsiderationAttributes json.RawMessage `json:"considerationAttributes"`
}

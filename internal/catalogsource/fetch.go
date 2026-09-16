package catalogsource

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/remiges-tech/logharbour/logharbour"
	"github.com/remiges-tushar/discovery-service/internal/beckn"
)

// listPageSize matches bpp-application's dashboard API's own max page size
// (internal/dashboardsvc/service.go: limit is clamped to 100 server-side),
// so a single request always gets the largest page that API will honor.
const listPageSize = 100

// HTTPSource polls configured BPP base URLs' dashboard API
// (GET /api/v1/catalogs, GET /api/v1/catalogs/{id}) for catalog data. See
// the package doc for why this API and not something more principled.
type HTTPSource struct {
	baseURLs []string
	client   *http.Client
	logger   *logharbour.Logger
}

// NewHTTPSource builds a source polling every baseURL in baseURLs. Each
// entry is used verbatim as a URL prefix (e.g. "http://bpp-app:8080", no
// trailing slash expected but tolerated).
func NewHTTPSource(baseURLs []string, client *http.Client, logger *logharbour.Logger) *HTTPSource {
	return &HTTPSource{baseURLs: baseURLs, client: client, logger: logger}
}

// FetchAll polls every configured source and returns the aggregated
// catalogs from whichever sources succeeded. anySuccess is false only when
// every configured source failed — callers should keep serving
// last-known-good data in that case rather than replacing it with an
// empty result (see Store.Refresh).
func (s *HTTPSource) FetchAll(ctx context.Context) (catalogs []beckn.Catalog, anySuccess bool) {
	for _, baseURL := range s.baseURLs {
		got, err := s.fetchSource(ctx, baseURL)
		if err != nil {
			s.logger.Warn().LogActivity("catalog source fetch failed", map[string]any{
				"baseUrl": baseURL,
				"error":   err.Error(),
			})
			continue
		}
		anySuccess = true
		catalogs = append(catalogs, got...)
	}
	return catalogs, anySuccess
}

func (s *HTTPSource) fetchSource(ctx context.Context, baseURL string) ([]beckn.Catalog, error) {
	ids, err := s.listCatalogIDs(ctx, baseURL)
	if err != nil {
		return nil, fmt.Errorf("list catalogs: %w", err)
	}

	catalogs := make([]beckn.Catalog, 0, len(ids))
	for _, id := range ids {
		detail, err := s.fetchCatalogDetail(ctx, baseURL, id)
		if err != nil {
			s.logger.Warn().LogActivity("catalog detail fetch failed, skipping this catalog", map[string]any{
				"baseUrl":   baseURL,
				"catalogId": id,
				"error":     err.Error(),
			})
			continue
		}
		catalogs = append(catalogs, toCatalog(*detail))
	}
	return catalogs, nil
}

// listCatalogIDs pages through GET {baseURL}/api/v1/catalogs until it has
// seen every item the source reports (via "total") or a page comes back
// short of a full page, whichever happens first.
func (s *HTTPSource) listCatalogIDs(ctx context.Context, baseURL string) ([]string, error) {
	var ids []string
	for page := 1; ; page++ {
		url := fmt.Sprintf("%s/api/v1/catalogs?page=%d&limit=%d", strings.TrimRight(baseURL, "/"), page, listPageSize)
		var resp catalogListResponse
		if err := s.getJSON(ctx, url, &resp); err != nil {
			return nil, err
		}
		for _, item := range resp.Items {
			ids = append(ids, item.ID)
		}
		if len(resp.Items) < listPageSize || len(ids) >= resp.Total {
			break
		}
	}
	return ids, nil
}

func (s *HTTPSource) fetchCatalogDetail(ctx context.Context, baseURL, catalogID string) (*catalogDetailResponse, error) {
	url := fmt.Sprintf("%s/api/v1/catalogs/%s", strings.TrimRight(baseURL, "/"), catalogID)
	var resp catalogDetailResponse
	if err := s.getJSON(ctx, url, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (s *HTTPSource) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s: unexpected status %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("GET %s: decode response: %w", url, err)
	}
	return nil
}

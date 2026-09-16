package dedicrawl

import (
	"context"
	"sync"
	"time"

	"github.com/remiges-tech/logharbour/logharbour"
	"github.com/remiges-tushar/discovery-service/internal/beckn"
)

// Store caches the catalogs a Crawler has indexed and implements
// service.CatalogStore. Catalogs() never does I/O — it returns the
// last-known-good snapshot, refreshed in the background by Start — so a
// slow or unreachable Provider Node never blocks a /discover request.
// This mirrors the earlier catalogsource.Store's contract exactly, so
// cmd/discovery/main.go's wiring around it barely changes.
type Store struct {
	crawler         *Crawler
	subscriberRefs  []string
	refreshInterval time.Duration
	logger          *logharbour.Logger

	mu       sync.RWMutex
	catalogs []beckn.Catalog
}

// NewStore builds a Store crawling every subscriberRef in subscriberRefs
// via crawler. Call Refresh once synchronously before serving traffic (so
// the first requests don't see an empty catalog if sources are
// reachable), then Start in a goroutine to keep it updated.
func NewStore(crawler *Crawler, subscriberRefs []string, refreshInterval time.Duration, logger *logharbour.Logger) *Store {
	return &Store{crawler: crawler, subscriberRefs: subscriberRefs, refreshInterval: refreshInterval, logger: logger}
}

// Catalogs returns a copy of the last successfully crawled catalog data
// (nil/empty if no crawl has ever succeeded yet).
func (s *Store) Catalogs() []beckn.Catalog {
	s.mu.RLock()
	defer s.mu.RUnlock()
	catalogs := make([]beckn.Catalog, len(s.catalogs))
	copy(catalogs, s.catalogs)
	return catalogs
}

// Refresh crawls every configured subscriber and, only if at least one
// succeeded, atomically replaces the cached catalogs. If every subscriber
// failed, the previous snapshot is kept as-is (stale data is better than
// no data) and the failure is logged by Crawler.CrawlAll's callers.
func (s *Store) Refresh(ctx context.Context) {
	catalogs, anySuccess := s.crawler.CrawlAll(ctx, s.subscriberRefs)
	if !anySuccess {
		s.logger.Err().LogActivity("catalog crawl: all subscribers failed, keeping previous data", nil)
		return
	}

	s.mu.Lock()
	s.catalogs = catalogs
	s.mu.Unlock()

	s.logger.LogActivity("catalog crawl succeeded", map[string]any{"catalogCount": len(catalogs)})
}

// Start runs Refresh on a fixed interval until ctx is cancelled. It does
// not run an initial Refresh itself — call Refresh once synchronously
// beforehand if the first requests should see fresh data as soon as
// possible.
func (s *Store) Start(ctx context.Context) {
	ticker := time.NewTicker(s.refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Refresh(ctx)
		}
	}
}

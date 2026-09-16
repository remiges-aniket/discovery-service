package catalogsource

import (
	"context"
	"sync"
	"time"

	"github.com/remiges-tech/logharbour/logharbour"
	"github.com/remiges-tushar/discovery-service/internal/beckn"
)

// Store caches the catalogs polled from an HTTPSource and implements
// service.CatalogStore. Catalogs() never does I/O — it returns the
// last-known-good snapshot, refreshed in the background by Start — so a
// slow or unreachable BPP source never blocks a /discover request (see
// CONTEXT.md D17 and service.CatalogStore's contract).
type Store struct {
	source          *HTTPSource
	refreshInterval time.Duration
	logger          *logharbour.Logger

	mu       sync.RWMutex
	catalogs []beckn.Catalog
}

// NewStore builds a Store around source. Call Refresh once synchronously
// before serving traffic (so the first requests don't see an empty
// catalog if the source is reachable), then Start in a goroutine to keep
// it updated.
func NewStore(source *HTTPSource, refreshInterval time.Duration, logger *logharbour.Logger) *Store {
	return &Store{source: source, refreshInterval: refreshInterval, logger: logger}
}

// Catalogs returns a copy of the last successfully fetched catalog data
// (nil/empty if no fetch has ever succeeded yet).
func (s *Store) Catalogs() []beckn.Catalog {
	s.mu.RLock()
	defer s.mu.RUnlock()
	catalogs := make([]beckn.Catalog, len(s.catalogs))
	copy(catalogs, s.catalogs)
	return catalogs
}

// Refresh fetches from every configured source and, only if at least one
// source succeeded, atomically replaces the cached catalogs. If every
// source failed, the previous snapshot is kept as-is (stale data is
// better than no data) and the failure is logged by HTTPSource.FetchAll.
func (s *Store) Refresh(ctx context.Context) {
	catalogs, anySuccess := s.source.FetchAll(ctx)
	if !anySuccess {
		s.logger.Err().LogActivity("catalog refresh: all sources failed, keeping previous data", nil)
		return
	}

	s.mu.Lock()
	s.catalogs = catalogs
	s.mu.Unlock()

	s.logger.LogActivity("catalog refresh succeeded", map[string]any{"catalogCount": len(catalogs)})
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

package service

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/remiges-tushar/discovery-service/internal/beckn"
	"github.com/remiges-tushar/discovery-service/internal/constants"
)

// catalogJSON is the M1 demo product catalog — a genuine, hand-authored
// JSON file (internal/service/data/catalog.json), not a hardcoded Go
// literal, so it can be edited/extended without touching Go source. It's
// embedded into the binary via go:embed rather than read from disk at
// runtime, for the same reason .env path resolution had to search
// upward (see CONTEXT.md D7 bugfix): a relative on-disk path is fragile
// to the process's working directory (repo root vs. cmd/discovery/ vs.
// a container's WORKDIR), and this file never needs to change without a
// rebuild anyway — real, editable-without-a-rebuild catalog data arrives
// with GORM/Postgres storage in M2.
//
//go:embed data/catalog.json
var catalogJSON []byte

// fixedCatalogs holds the parsed contents of data/catalog.json, parsed
// once at package init so a malformed file fails fast at process
// startup (a clear panic naming the JSON error) rather than on the
// first /discover request.
var fixedCatalogs = mustParseFixedCatalogs()

func mustParseFixedCatalogs() []beckn.Catalog {
	var doc struct {
		Catalogs []beckn.Catalog `json:"catalogs"`
	}
	if err := json.Unmarshal(catalogJSON, &doc); err != nil {
		panic(fmt.Sprintf("service: data/catalog.json is invalid JSON: %v", err))
	}
	if len(doc.Catalogs) == 0 {
		panic("service: data/catalog.json defines no catalogs")
	}
	return doc.Catalogs
}

// BuildFixedOnDiscover builds the M1 canned on_discover payload from the
// embedded demo catalog (data/catalog.json), regardless of the request's
// intent. Kept as a thin wrapper around BuildOnDiscover so the original M1
// tests (and any caller wanting the fixed demo data specifically) are
// unaffected by CONTEXT.md D17's addition of external catalog sources —
// see EmbeddedCatalogStore for the CatalogStore-backed equivalent used by
// DiscoverService today.
func BuildFixedOnDiscover(reqCtx beckn.Context, ownBppID, ownBppURI string) beckn.OnDiscoverRequest {
	return BuildOnDiscover(reqCtx, ownBppID, ownBppURI, fixedCatalogs)
}

// BuildOnDiscover builds the on_discover payload for reqCtx from the given
// catalogs — the demo fixed catalog (BuildFixedOnDiscover), or real data
// polled from configured BPP sources (see internal/catalogsource,
// CONTEXT.md D17), depending on which CatalogStore DiscoverService was
// constructed with. It returns the same payload shape regardless of the
// catalogs' origin; real query matching against them is a later milestone
// (M3).
//
// ownBppID/ownBppURI are this service's own registered identity (see
// config.Config.BppID/BppURI, CONTEXT.md D13). A real observed /discover
// request often carries NO context.bppId/bppUri at all (the BAP is
// calling a network-wide discovery service, not addressing one specific
// BPP) — in that case the responding service must stamp its own identity
// onto the on_discover context rather than leaving it blank. If the
// request DID already address a specific bppId/bppUri, that is echoed
// back unchanged; it is never ours to overwrite.
//
// Individual catalogs may likewise already declare their own
// bppId/bppUri (a fan-out/federated result originating from a different
// BPP than the one answering this request) — those are preserved as-is;
// only catalogs that omit one inherit the response context's identity.
//
// Each call returns its own copy of catalogs, so concurrent callers never
// observe or mutate each other's values (see
// TestBuildFixedOnDiscover_DoesNotMutateSharedCatalogDataAcrossCalls).
func BuildOnDiscover(reqCtx beckn.Context, ownBppID, ownBppURI string, catalogs []beckn.Catalog) beckn.OnDiscoverRequest {
	respCtx := reqCtx
	respCtx.Action = constants.ActionOnDiscover
	if respCtx.BppID == "" {
		respCtx.BppID = ownBppID
	}
	if respCtx.BppURI == "" {
		respCtx.BppURI = ownBppURI
	}

	out := make([]beckn.Catalog, len(catalogs))
	for i, c := range catalogs {
		if c.BppID == "" {
			c.BppID = respCtx.BppID
		}
		if c.BppURI == "" {
			c.BppURI = respCtx.BppURI
		}
		out[i] = c
	}

	return beckn.OnDiscoverRequest{
		Context: respCtx,
		Message: beckn.OnDiscoverMessage{Catalogs: out},
	}
}

// EmbeddedCatalogStore is the zero-config default CatalogStore: it always
// returns the embedded demo catalog (data/catalog.json), unchanged from
// M1. DiscoverService falls back to this when no external catalog sources
// are configured (config.Config.CatalogSourceURLs empty) — see CONTEXT.md
// D17.
type EmbeddedCatalogStore struct{}

// Catalogs returns a copy of the embedded demo catalog data so callers
// can never mutate the shared package-level slice.
func (EmbeddedCatalogStore) Catalogs() []beckn.Catalog {
	catalogs := make([]beckn.Catalog, len(fixedCatalogs))
	copy(catalogs, fixedCatalogs)
	return catalogs
}

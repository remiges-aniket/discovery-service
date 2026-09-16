package service

import (
	"context"
	"encoding/json"
	"math"
	"strings"

	"github.com/theory/jsonpath"

	"github.com/remiges-tushar/discovery-service/internal/beckn"
)

// earthRadiusMeters is used for the Haversine distance calc backing the
// only spatial operator this package understands (opSDWithin) — see
// matchesSpatial.
const earthRadiusMeters = 6371000.0

// parseJSONPathFilter parses expression as an RFC 9535 JSONPath query.
// Shared by validateIntent (internal/service/discover.go, so a malformed
// expression is rejected with a 400 before the request is even Acked) and
// matchCatalogs (which assumes validateIntent already ran and this will
// succeed — see matchCatalogs' doc comment for the defensive fallback if
// that assumption is ever violated).
func parseJSONPathFilter(expression string) (*jsonpath.Path, error) {
	return jsonpath.Parse(expression)
}

// matchCatalogs narrows catalogs down to what intent actually asks for:
// spatial constraints against each catalog's provider locations, textSearch
// and JSONPath filters against its resources (dropping offers that only
// referenced now-excluded resources), per the documented /discover contract
// (CONTEXT.md §6). A catalog with nothing left afterward (no resources, no
// offers) is dropped entirely, since beckn.yaml requires at least one of
// either.
//
// mediaSearch is intentionally not evaluated — there is no image/audio
// similarity capability available in-memory; a request that sets it gets
// unfiltered results for that dimension rather than a silent pretense of
// support.
//
// If intent is the zero value (nothing set on it), catalogs is returned
// unchanged with no copying — every existing caller that doesn't care about
// intent keeps working exactly as before.
//
// ctx bounds how long this is allowed to run (see CONTEXT.md D20 — a
// caller-supplied filter now does real work per request, D19, and inbound
// requests aren't authenticated yet, D15, so nothing else stops one
// expensive request from tying up CPU indefinitely). It's checked once per
// catalog in the main loop below; if it's done, matching stops immediately
// and whatever's been matched so far is returned rather than the full set
// — a partial-but-bounded result instead of an unbounded one.
func matchCatalogs(ctx context.Context, catalogs []beckn.Catalog, intent beckn.Intent) []beckn.Catalog {
	if isEmptyIntent(intent) {
		return catalogs
	}

	var filterPath *jsonpath.Path
	if intent.Filters != nil {
		// Assumed already validated (see validateIntent) by the time a
		// request reaches here — Validate/ValidateSync both reject an
		// unparseable expression with 400 before any catalog matching
		// runs. If this ever fails anyway (a defensive fallback, not a
		// reachable path in normal operation), treat the filter as absent
		// rather than erroring deep inside response building with no way
		// to surface a 400 any more (the async path has already Acked).
		if p, err := parseJSONPathFilter(intent.Filters.Expression); err == nil {
			filterPath = p
		}
	}

	out := make([]beckn.Catalog, 0, len(catalogs))
	for _, cat := range catalogs {
		if ctx.Err() != nil {
			break
		}
		if len(intent.Spatial) > 0 && !matchesAllSpatial(cat, intent.Spatial) {
			continue
		}

		resources := cat.Resources
		if intent.TextSearch != "" {
			resources = filterResourcesByText(resources, intent.TextSearch)
		}
		if filterPath != nil {
			resources = filterResourcesByJSONPath(resources, filterPath)
		}

		offers := filterOffersByResources(cat.Offers, resources)

		if len(resources) == 0 && len(offers) == 0 {
			continue
		}
		cat.Resources = resources
		cat.Offers = offers
		out = append(out, cat)
	}
	return out
}

func isEmptyIntent(intent beckn.Intent) bool {
	return intent.TextSearch == "" && intent.Filters == nil && len(intent.Spatial) == 0 && intent.MediaSearch == nil
}

// filterResourcesByText keeps resources where every whitespace-separated
// term in textSearch appears (case-insensitively) somewhere in the
// resource's descriptor name/shortDesc/longDesc.
func filterResourcesByText(resources []beckn.Resource, textSearch string) []beckn.Resource {
	terms := strings.Fields(strings.ToLower(textSearch))
	if len(terms) == 0 {
		return resources
	}

	out := make([]beckn.Resource, 0, len(resources))
	for _, r := range resources {
		haystack := strings.ToLower(r.Descriptor.Name + " " + r.Descriptor.ShortDesc + " " + r.Descriptor.LongDesc)
		matched := true
		for _, term := range terms {
			if !strings.Contains(haystack, term) {
				matched = false
				break
			}
		}
		if matched {
			out = append(out, r)
		}
	}
	return out
}

// filterResourcesByJSONPath keeps resources whose "id" appears among the
// results of evaluating path against resources as a JSON array — the
// natural way to apply a filter-selector expression like
// "$[?(@.rating.value >= 4.0)]" (beckn.yaml's own example) across a set of
// items.
func filterResourcesByJSONPath(resources []beckn.Resource, path *jsonpath.Path) []beckn.Resource {
	raw, err := json.Marshal(resources)
	if err != nil {
		// Resource always marshals cleanly (it's our own wire type) — this
		// is unreachable in practice, but fail safe (no matches) rather
		// than panic.
		return nil
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil
	}

	matchedIDs := make(map[string]bool)
	for _, node := range path.Select(generic) {
		obj, ok := node.(map[string]any)
		if !ok {
			continue
		}
		if id, ok := obj["id"].(string); ok {
			matchedIDs[id] = true
		}
	}

	out := make([]beckn.Resource, 0, len(resources))
	for _, r := range resources {
		if matchedIDs[r.ID] {
			out = append(out, r)
		}
	}
	return out
}

// filterOffersByResources keeps an Offer if it doesn't reference specific
// resources at all (a catalog-wide offer) or references at least one
// resource that's still present after text/filter matching.
func filterOffersByResources(offers []beckn.Offer, survivingResources []beckn.Resource) []beckn.Offer {
	survivingIDs := make(map[string]bool, len(survivingResources))
	for _, r := range survivingResources {
		survivingIDs[r.ID] = true
	}

	out := make([]beckn.Offer, 0, len(offers))
	for _, o := range offers {
		if len(o.ResourceIDs) == 0 {
			out = append(out, o)
			continue
		}
		for _, rid := range o.ResourceIDs {
			if survivingIDs[rid] {
				out = append(out, o)
				break
			}
		}
	}
	return out
}

// opSDWithin is the only spatial operator this package evaluates — a
// distance-based constraint, the one worked example documented in
// CONTEXT.md §6. Any other op is treated as permissively passing (see
// matchesSpatial) rather than rejected, since full CQL2 operator/geometry
// coverage is out of scope for this pass.
const opSDWithin = "S_DWITHIN"

// matchesAllSpatial reports whether cat satisfies every constraint in
// constraints (AND across constraints).
func matchesAllSpatial(cat beckn.Catalog, constraints []beckn.SpatialConstraint) bool {
	for _, c := range constraints {
		if !matchesSpatial(cat, c) {
			return false
		}
	}
	return true
}

// matchesSpatial evaluates one constraint against cat.Provider.AvailableAt.
// Only op=="S_DWITHIN" with a Point geometry and DistanceMeters set is
// understood; anything else passes permissively (an unsupported operator
// must never silently exclude every catalog from every request that uses
// it).
func matchesSpatial(cat beckn.Catalog, c beckn.SpatialConstraint) bool {
	if c.Op != opSDWithin || c.Geometry == nil || c.Geometry.Type != "Point" || c.DistanceMeters == nil {
		return true
	}
	targetLon, targetLat, ok := pointCoordinates(c.Geometry.Coordinates)
	if !ok {
		return true
	}

	quantifier := c.Quantifier
	if quantifier == "" {
		quantifier = "ANY"
	}

	total, within := 0, 0
	for _, loc := range cat.Provider.AvailableAt {
		if loc.Geo.Type != "Point" {
			continue
		}
		lon, lat, ok := pointCoordinates(loc.Geo.Coordinates)
		if !ok {
			continue
		}
		total++
		if haversineMeters(lat, lon, targetLat, targetLon) <= *c.DistanceMeters {
			within++
		}
	}
	if total == 0 {
		// No point geometries to evaluate against at all — permissive,
		// same rationale as an unsupported op/geometry above.
		return true
	}

	switch strings.ToUpper(quantifier) {
	case "ALL":
		return within == total
	default: // "ANY"
		return within > 0
	}
}

// pointCoordinates extracts [lon, lat] from a GeoJSON Point's generic
// (any-typed, JSON-decoded) Coordinates field.
func pointCoordinates(coordinates any) (lon, lat float64, ok bool) {
	coords, ok := coordinates.([]any)
	if !ok || len(coords) < 2 {
		return 0, 0, false
	}
	lonF, ok1 := toFloat(coords[0])
	latF, ok2 := toFloat(coords[1])
	if !ok1 || !ok2 {
		return 0, 0, false
	}
	return lonF, latF, true
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// haversineMeters computes the great-circle distance between two
// lat/lon points in meters.
func haversineMeters(lat1, lon1, lat2, lon2 float64) float64 {
	toRad := func(deg float64) float64 { return deg * math.Pi / 180 }
	dLat := toRad(lat2 - lat1)
	dLon := toRad(lon2 - lon1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(toRad(lat1))*math.Cos(toRad(lat2))*math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthRadiusMeters * c
}

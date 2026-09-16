package dedicrawl

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/remiges-tech/logharbour/logharbour"
	"github.com/remiges-tushar/discovery-service/internal/beckn"
)

// defaultCutoverFraction is used when Crawler is constructed with a
// non-positive fraction (config validation stopgap — see NewCrawler).
const defaultCutoverFraction = 0.5

// Crawler implements protocol-specifications-v2 §10.2's verify-then-index
// pipeline: Registry manifest -> subscriber record -> catalog index ->
// baseline/change files, verifying a signature or digest at every hop and
// discarding (never trusting) anything that fails.
//
// A single Crawler is meant to be long-lived across repeated crawls (see
// Store): its cursorState remembers each catalog's last-indexed version
// and content, so unchanged catalogs are skipped without re-fetching, and
// a retiredAt observation is never reversed by a later crawl that simply
// omits the entry (§10.5).
type Crawler struct {
	registry        Registry
	registryBaseURL string
	client          *http.Client
	logger          *logharbour.Logger
	cursor          *cursorState

	// networkIDs/schemaTypes optionally scope which index entries this DS
	// indexes at all (§10.2's index-level filtering). Empty means "accept
	// everything" — no scoping configured.
	networkIDs  []string
	schemaTypes []string

	// cutoverFraction is the §10.1 cutover rule's threshold: fetch the
	// baseline instead of the accumulated change-file chain once the
	// chain's combined size exceeds this fraction of the baseline size.
	cutoverFraction float64
}

// NewCrawler builds a Crawler. cutoverFraction <= 0 falls back to
// defaultCutoverFraction rather than disabling the rule entirely (a
// zero/negative threshold would mean "always cut over", which silently
// defeats the point of change files).
func NewCrawler(registry Registry, registryBaseURL string, client *http.Client, logger *logharbour.Logger, networkIDs, schemaTypes []string, cutoverFraction float64) *Crawler {
	if cutoverFraction <= 0 {
		cutoverFraction = defaultCutoverFraction
	}
	return &Crawler{
		registry:        registry,
		registryBaseURL: registryBaseURL,
		client:          client,
		logger:          logger,
		cursor:          newCursorState(),
		networkIDs:      networkIDs,
		schemaTypes:     schemaTypes,
		cutoverFraction: cutoverFraction,
	}
}

// CrawlAll crawls every subscriberRef and returns the full current
// catalog snapshot (across all subscribers ever crawled by this Crawler,
// since cursorState is cumulative). anySuccess is false only when every
// subscriber failed outright (e.g. unreachable, manifest verification
// failed) — callers should keep serving the previous snapshot rather than
// replacing it with this (possibly empty) one in that case, same
// contract as the old catalogsource.HTTPSource.FetchAll.
func (c *Crawler) CrawlAll(ctx context.Context, subscriberRefs []string) (catalogs []beckn.Catalog, anySuccess bool) {
	for _, ref := range subscriberRefs {
		if err := c.CrawlSubscriber(ctx, ref); err != nil {
			c.logger.Warn().LogActivity("catalog crawl failed", map[string]any{
				"subscriberRef": ref,
				"error":         err.Error(),
			})
			continue
		}
		anySuccess = true
	}
	return c.cursor.snapshot(), anySuccess
}

// CrawlSubscriber runs the full §10.2 pipeline for one Provider Node.
func (c *Crawler) CrawlSubscriber(ctx context.Context, subscriberRef string) error {
	manifest, err := c.registry.Manifest(ctx, c.registryBaseURL)
	if err != nil {
		return fmt.Errorf("fetch registry manifest: %w", err)
	}
	registryKey, err := verifyManifest(manifest)
	if err != nil {
		return fmt.Errorf("verify registry manifest: %w", err)
	}
	_ = registryKey // anchors trust in the registry; the stub's subscriber lookup is itself the trust boundary for now (see registry.go).

	record, subscriberKey, err := c.registry.SubscriberRecord(ctx, subscriberRef)
	if err != nil {
		return fmt.Errorf("fetch subscriber record: %w", err)
	}
	if err := verifySubscriberRecord(record); err != nil {
		return fmt.Errorf("verify subscriber record %q: %w", subscriberRef, err)
	}

	var indexed, attempted int
	anyIndexFetchedOK := false
	seen := make(map[string]bool)
	for _, indexURL := range record.CatalogIndexURLs {
		n, a, err := c.crawlIndex(ctx, indexURL, subscriberRef, subscriberKey, seen)
		if err != nil {
			c.logger.Warn().LogActivity("catalog index crawl failed, skipping this index", map[string]any{
				"subscriberRef": subscriberRef,
				"indexUrl":      indexURL,
				"error":         err.Error(),
			})
			continue
		}
		anyIndexFetchedOK = true
		indexed += n
		attempted += a
	}
	if indexed == 0 {
		if !anyIndexFetchedOK {
			return fmt.Errorf("failed to fetch any catalog index from %d url(s)", len(record.CatalogIndexURLs))
		}
		if attempted > 0 {
			return fmt.Errorf("no catalog index entries indexed out of %d attempted", attempted)
		}
		// At least one index fetched and parsed fine but declared zero
		// in-scope entries — a legitimately empty index, not a failure.
	}

	// This subscriber's crawl succeeded overall — a catalogId previously
	// indexed for it but absent from every index just fetched (without a
	// signed retiredAt tombstone) stops being served until reconfirmed
	// (CON-TBD-27/CON-TBD-35), without erasing its cursor state.
	c.cursor.markUnseen(subscriberRef, seen)
	return nil
}

// crawlIndex fetches and applies one catalog index. It returns indexed (how
// many in-scope entries were successfully applied) and attempted (how many
// in-scope entries were tried at all, whether or not they succeeded) — the
// two are compared by the caller to distinguish "index legitimately declared
// nothing in scope" from "entries existed but all failed verification".
func (c *Crawler) crawlIndex(ctx context.Context, indexURL, subscriberRef string, subscriberKey ed25519.PublicKey, seen map[string]bool) (indexed, attempted int, err error) {
	raw, err := c.fetchBytes(ctx, indexURL)
	if err != nil {
		return 0, 0, fmt.Errorf("fetch catalog index: %w", err)
	}
	var index CatalogIndex
	if err := json.Unmarshal(raw, &index); err != nil {
		return 0, 0, fmt.Errorf("decode catalog index: %w", err)
	}

	ordered := orderMastersFirst(index.Entries)

	for _, entry := range ordered {
		if err := verifyIndexEntrySignature(entry, subscriberKey); err != nil {
			attempted++
			c.logger.Warn().LogActivity("catalog index entry signature invalid, discarding", map[string]any{
				"catalogId": entry.CatalogID,
				"error":     err.Error(),
			})
			continue
		}
		if !c.inScope(entry) {
			// Deliberately excluded by this DS's own scope filters — not an
			// attempt that failed, so it doesn't count toward attempted.
			continue
		}
		attempted++
		seen[entry.CatalogID] = true
		if err := c.applyEntry(ctx, entry, subscriberRef, subscriberKey); err != nil {
			c.logger.Warn().LogActivity("catalog entry discarded", map[string]any{
				"catalogId": entry.CatalogID,
				"error":     err.Error(),
			})
			continue
		}
		indexed++
	}
	return indexed, attempted, nil
}

// orderMastersFirst returns entries with CatalogType==MASTER before all
// others, preserving relative order within each group, so a REGULAR
// catalog's dependency is always indexed before it.
func orderMastersFirst(entries []IndexEntry) []IndexEntry {
	out := make([]IndexEntry, 0, len(entries))
	for _, e := range entries {
		if e.CatalogType == CatalogTypeMaster {
			out = append(out, e)
		}
	}
	for _, e := range entries {
		if e.CatalogType != CatalogTypeMaster {
			out = append(out, e)
		}
	}
	return out
}

func (c *Crawler) inScope(entry IndexEntry) bool {
	if len(c.networkIDs) > 0 && !intersects(c.networkIDs, entry.NetworkIDs) {
		return false
	}
	if len(c.schemaTypes) > 0 && !intersects(c.schemaTypes, entry.SchemaTypes) {
		return false
	}
	return true
}

func intersects(want, have []string) bool {
	set := make(map[string]struct{}, len(have))
	for _, v := range have {
		set[v] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[w]; ok {
			return true
		}
	}
	return false
}

// applyEntry advances the cursor for entry.CatalogID: honoring a signed
// retiredAt permanently, skipping a fetch when entryVersion is unchanged,
// and otherwise fetching + verifying + applying the baseline or change
// file(s) that bring the catalog up to entry.EntryVersion (§10.1/§10.5).
func (c *Crawler) applyEntry(ctx context.Context, entry IndexEntry, subscriberRef string, subscriberKey ed25519.PublicKey) error {
	prev, known := c.cursor.get(entry.CatalogID)
	if prev.retired {
		// Permanent tombstone: never resurrected by a later crawl that
		// simply omits/repeats an entry (§10.5).
		return nil
	}

	if entry.RetiredAt != nil {
		c.cursor.put(entry.CatalogID, cursorEntry{
			entryVersion:  entry.EntryVersion,
			isActive:      false,
			retired:       true,
			subscriberRef: subscriberRef,
		})
		return nil
	}

	if known && versionRegressed(prev.entryVersion, entry.EntryVersion) {
		// CON-TBD-03/04/11: versions MUST be monotonic; an entryVersion
		// that regresses relative to our cursor is discarded rather than
		// applied (best-effort — only enforced when both versions parse as
		// integers, since this crawler treats entryVersion as an opaque
		// string more generally).
		return fmt.Errorf("entryVersion regressed: cursor at %q, index declares %q", prev.entryVersion, entry.EntryVersion)
	}

	if known && prev.entryVersion == entry.EntryVersion {
		// Unchanged: keep prior content, just refresh isActive in case a
		// pause/unpause happened without a version bump, and reconfirm it
		// (clears any unconfirmed flag from a prior crawl where this
		// catalogId was absent — see cursorState.markUnseen).
		prev.isActive = entry.IsActive
		prev.subscriberRef = subscriberRef
		prev.unconfirmed = false
		c.cursor.put(entry.CatalogID, prev)
		return nil
	}

	catalog, newVersion, err := c.resolveCatalog(ctx, entry, prev, known, subscriberKey)
	if err != nil {
		return err
	}

	c.cursor.put(entry.CatalogID, cursorEntry{
		entryVersion:  newVersion,
		isActive:      entry.IsActive,
		catalog:       catalog,
		subscriberRef: subscriberRef,
	})
	return nil
}

// versionRegressed reports whether newVersion is numerically smaller than
// prevVersion. Both must parse as base-10 integers for the check to apply;
// otherwise (e.g. this crawler's own opaque version strings like "v1"/"v2"
// used before a real Registry integration exists) it reports false — no
// false positives on non-numeric version schemes.
func versionRegressed(prevVersion, newVersion string) bool {
	prev, err1 := strconv.ParseInt(prevVersion, 10, 64)
	next, err2 := strconv.ParseInt(newVersion, 10, 64)
	if err1 != nil || err2 != nil {
		return false
	}
	return next < prev
}

// resolveCatalog fetches whatever's needed to bring entry.CatalogID's
// content up to entry.EntryVersion: the baseline file on first sight or
// when the cutover rule fires, otherwise the chained change files since
// the stored cursor.
func (c *Crawler) resolveCatalog(ctx context.Context, entry IndexEntry, prev cursorEntry, known bool, subscriberKey ed25519.PublicKey) (beckn.Catalog, string, error) {
	if !known {
		return c.fetchBaseline(ctx, entry, subscriberKey)
	}

	chain, ok := buildChangeChain(entry.Changes, prev.entryVersion, entry.EntryVersion)
	if !ok {
		// No contiguous chain from our cursor to the current baseline
		// version — the PN may have compacted history. Fall back to
		// baseline.
		return c.fetchBaseline(ctx, entry, subscriberKey)
	}

	var chainSize int64
	for _, cr := range chain {
		chainSize += cr.Size
	}
	if entry.Baseline.Size > 0 && float64(chainSize) > c.cutoverFraction*float64(entry.Baseline.Size) {
		return c.fetchBaseline(ctx, entry, subscriberKey)
	}

	catalog := prev.catalog
	for _, cr := range chain {
		delta, err := c.fetchChangeFile(ctx, entry.CatalogID, cr, subscriberKey)
		if err != nil {
			return beckn.Catalog{}, "", err
		}
		catalog = applyChange(catalog, delta)
	}
	return catalog, entry.EntryVersion, nil
}

// buildChangeChain walks entry.Changes to find a contiguous sequence
// from fromVersion to toVersion. ok is false if no such chain exists
// (e.g. the PN compacted history past our cursor).
func buildChangeChain(changes []ChangeRef, fromVersion, toVersion string) (chain []ChangeRef, ok bool) {
	byFrom := make(map[string]ChangeRef, len(changes))
	for _, cr := range changes {
		byFrom[cr.FromVersion] = cr
	}
	cursor := fromVersion
	for cursor != toVersion {
		cr, found := byFrom[cursor]
		if !found {
			return nil, false
		}
		chain = append(chain, cr)
		cursor = cr.ToVersion
	}
	return chain, true
}

func (c *Crawler) fetchBaseline(ctx context.Context, entry IndexEntry, subscriberKey ed25519.PublicKey) (beckn.Catalog, string, error) {
	raw, err := c.fetchBytes(ctx, entry.Baseline.URL)
	if err != nil {
		return beckn.Catalog{}, "", fmt.Errorf("fetch baseline: %w", err)
	}
	if err := VerifyDigest(raw, entry.Baseline.Digest); err != nil {
		return beckn.Catalog{}, "", fmt.Errorf("baseline: %w", err)
	}
	var file CatalogFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return beckn.Catalog{}, "", fmt.Errorf("decode baseline: %w", err)
	}
	if file.CatalogID != entry.CatalogID || file.Version != entry.Baseline.Version {
		return beckn.Catalog{}, "", fmt.Errorf("baseline catalogId/version %q/%q does not match index entry %q/%q",
			file.CatalogID, file.Version, entry.CatalogID, entry.Baseline.Version)
	}
	if err := verifyCatalogFileSignature(file, subscriberKey); err != nil {
		return beckn.Catalog{}, "", fmt.Errorf("baseline signature: %w", err)
	}
	return file.Catalog, entry.Baseline.Version, nil
}

func (c *Crawler) fetchChangeFile(ctx context.Context, catalogID string, ref ChangeRef, subscriberKey ed25519.PublicKey) (CatalogChangeFile, error) {
	raw, err := c.fetchBytes(ctx, ref.URL)
	if err != nil {
		return CatalogChangeFile{}, fmt.Errorf("fetch change file: %w", err)
	}
	if err := VerifyDigest(raw, ref.Digest); err != nil {
		return CatalogChangeFile{}, fmt.Errorf("change file: %w", err)
	}
	var change CatalogChangeFile
	if err := json.Unmarshal(raw, &change); err != nil {
		return CatalogChangeFile{}, fmt.Errorf("decode change file: %w", err)
	}
	if change.CatalogID != catalogID || change.FromVersion != ref.FromVersion || change.ToVersion != ref.ToVersion {
		return CatalogChangeFile{}, fmt.Errorf("change file catalogId/version %q/%q->%q does not match index entry %q/%q->%q",
			change.CatalogID, change.FromVersion, change.ToVersion, catalogID, ref.FromVersion, ref.ToVersion)
	}
	if err := verifyCatalogChangeFileSignature(change, subscriberKey); err != nil {
		return CatalogChangeFile{}, fmt.Errorf("change file signature: %w", err)
	}
	return change, nil
}

// applyChange merges delta's upserts/removals onto base, replacing any
// catalog-level fields delta declares.
func applyChange(base beckn.Catalog, delta CatalogChangeFile) beckn.Catalog {
	if delta.Catalog != nil {
		base = *delta.Catalog
	}

	resources := make(map[string]beckn.Resource, len(base.Resources))
	for _, r := range base.Resources {
		resources[r.ID] = r
	}
	for _, r := range delta.Resources.Upserts {
		resources[r.ID] = r
	}
	for _, id := range delta.Resources.Removals {
		delete(resources, id)
	}
	base.Resources = mapValuesResources(resources)

	offers := make(map[string]beckn.Offer, len(base.Offers))
	for _, o := range base.Offers {
		offers[o.ID] = o
	}
	for _, o := range delta.Offers.Upserts {
		offers[o.ID] = o
	}
	for _, id := range delta.Offers.Removals {
		delete(offers, id)
	}
	base.Offers = mapValuesOffers(offers)

	return base
}

func mapValuesResources(m map[string]beckn.Resource) []beckn.Resource {
	out := make([]beckn.Resource, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func mapValuesOffers(m map[string]beckn.Offer) []beckn.Offer {
	out := make([]beckn.Offer, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func (c *Crawler) fetchBytes(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: unexpected status %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// --- signature verification helpers (each signs/verifies its target
// struct's JCS-canonical form with its own Signature field cleared) ---

func verifyManifest(m DediManifest) (ed25519.PublicKey, error) {
	pub, err := base64.StdEncoding.DecodeString(m.RegistryPublicKeyBase64)
	if err != nil {
		return nil, fmt.Errorf("decode registry public key: %w", err)
	}
	sig := m.Signature
	m.Signature = ""
	canonical, err := CanonicalizeJCS(m)
	if err != nil {
		return nil, err
	}
	if err := VerifyDetached(pub, canonical, sig); err != nil {
		return nil, err
	}
	return ed25519.PublicKey(pub), nil
}

// verifySubscriberRecord checks the record's self-declared digest against
// its own canonical content (digest field cleared). This is a
// deliberately simplified trust check for the StubRegistry-only
// implementation in this milestone — a real Registry integration would
// additionally need the manifest to anchor the record's authenticity
// (e.g. via a registry-side signature over the record), not just an
// internally-consistent digest.
func verifySubscriberRecord(record SubscriberRecord) error {
	want := record.Digest
	record.Digest = ""
	canonical, err := CanonicalizeJCS(record)
	if err != nil {
		return err
	}
	return VerifyDigest(canonical, want)
}

func verifyIndexEntrySignature(entry IndexEntry, pubKey ed25519.PublicKey) error {
	sig := entry.Signature
	entry.Signature = ""
	canonical, err := CanonicalizeJCS(entry)
	if err != nil {
		return err
	}
	return VerifyDetached(pubKey, canonical, sig)
}

func verifyCatalogFileSignature(file CatalogFile, pubKey ed25519.PublicKey) error {
	sig := file.Signature
	file.Signature = ""
	canonical, err := CanonicalizeJCS(file)
	if err != nil {
		return err
	}
	return VerifyDetached(pubKey, canonical, sig)
}

func verifyCatalogChangeFileSignature(change CatalogChangeFile, pubKey ed25519.PublicKey) error {
	sig := change.Signature
	change.Signature = ""
	canonical, err := CanonicalizeJCS(change)
	if err != nil {
		return err
	}
	return VerifyDetached(pubKey, canonical, sig)
}

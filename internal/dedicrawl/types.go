// Package dedicrawl implements the decentralized catalog crawler described
// in protocol-specifications-v2's Catalog_Publishing_and_Discovery.md §10:
// a Discovery Service crawls Provider Node-hosted, self-signed catalog
// files (discovered via each PN's Registry Subscriber record) instead of
// receiving a push. It replaces the earlier internal/catalogsource
// stopgap (D17), which polled a BPP's internal dashboard API and carried
// no authentication, versioning, or spec compliance.
//
// See internal/service.CatalogStore: dedicrawl.Store implements that same
// port, so nothing outside this package needs to change to consume it.
package dedicrawl

import "github.com/remiges-tushar/discovery-service/internal/beckn"

// Catalog types (MASTER extends via Dependencies; REGULAR is a leaf).
const (
	CatalogTypeMaster  = "MASTER"
	CatalogTypeRegular = "REGULAR"
)

// CatalogFile is a Provider Node's self-signed baseline catalog document
// (§10.1). It wraps the existing beckn.Catalog schema unmodified.
type CatalogFile struct {
	CatalogID  string        `json:"catalogId"`
	Version    string        `json:"version"`
	NextUpdate string        `json:"next_update,omitempty"`
	Catalog    beckn.Catalog `json:"catalog"`
	RetiredAt  *string       `json:"retiredAt,omitempty"`
	Signature  string        `json:"signature"`
}

// ResourceDelta is the resources half of a CatalogChangeFile.
type ResourceDelta struct {
	Upserts  []beckn.Resource `json:"upserts,omitempty"`
	Removals []string         `json:"removals,omitempty"`
}

// OfferDelta is the offers half of a CatalogChangeFile.
type OfferDelta struct {
	Upserts  []beckn.Offer `json:"upserts,omitempty"`
	Removals []string      `json:"removals,omitempty"`
}

// CatalogChangeFile is a Provider Node's self-signed incremental delta
// against a prior version (§10.1).
type CatalogChangeFile struct {
	CatalogID   string         `json:"catalogId"`
	FromVersion string         `json:"fromVersion"`
	ToVersion   string         `json:"toVersion"`
	Resources   ResourceDelta  `json:"resources"`
	Offers      OfferDelta     `json:"offers"`
	Catalog     *beckn.Catalog `json:"catalog,omitempty"`
	Signature   string         `json:"signature"`
}

// FileRef points at a signed, content-addressed baseline or "latest" file.
type FileRef struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	Size    int64  `json:"size"`
	Digest  string `json:"digest"`
}

// ChangeRef points at one signed change file spanning a version range.
type ChangeRef struct {
	FromVersion string `json:"fromVersion"`
	ToVersion   string `json:"toVersion"`
	URL         string `json:"url"`
	Size        int64  `json:"size"`
	Digest      string `json:"digest"`
}

// MasterRef is a REGULAR catalog's declared dependency on a MASTER
// catalog it extends.
type MasterRef struct {
	CatalogID string `json:"catalogId"`
	Version   string `json:"version"`
	IndexURL  string `json:"indexUrl"`
}

// IndexEntry is one Provider Node's declaration, within its catalog
// index, of a single catalog's current publication state (§10.1).
type IndexEntry struct {
	CatalogID    string      `json:"catalogId"`
	EntryVersion string      `json:"entryVersion"`
	CatalogType  string      `json:"catalogType"`
	Dependencies struct {
		Masters []MasterRef `json:"masters,omitempty"`
	} `json:"dependencies"`
	NetworkIDs  []string   `json:"networkIds,omitempty"`
	SchemaTypes []string   `json:"schemaTypes,omitempty"`
	IsActive    bool       `json:"isActive"`
	Baseline    FileRef    `json:"baseline"`
	Changes     []ChangeRef `json:"changes,omitempty"`
	Latest      *FileRef   `json:"latest,omitempty"`
	RetiredAt   *string    `json:"retiredAt,omitempty"`
	Signature   string     `json:"signature"`
}

// CatalogIndex is the top-level document served at one of a Subscriber
// record's catalog_index_urls entries.
type CatalogIndex struct {
	Entries []IndexEntry `json:"entries"`
}

// DediManifest is the Registry-hosted manifest fetched from
// /.well-known/dedi.json — only the fields the crawl pipeline needs.
type DediManifest struct {
	RegistryPublicKeyBase64 string `json:"registryPublicKeyBase64"`
	Signature               string `json:"signature"`
}

// SubscriberRecord is the Registry-hosted record for one Provider Node —
// only the fields the crawl pipeline needs (its full public-key material
// and self-declared catalog index locations).
type SubscriberRecord struct {
	SubscriberID     string   `json:"subscriberId"`
	Digest           string   `json:"digest"`
	PublicKeyBase64  string   `json:"publicKeyBase64"`
	CatalogIndexURLs []string `json:"catalog_index_urls"`
}

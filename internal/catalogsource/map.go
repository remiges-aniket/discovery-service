package catalogsource

import (
	"encoding/json"

	"github.com/remiges-tushar/discovery-service/internal/beckn"
)

// toCatalog maps one BPP dashboard-API catalog detail response into a
// beckn.Catalog. bppId/bppUri are deliberately left empty here — the BPP's
// dashboard API doesn't expose its own registered network identity, so
// BuildOnDiscover's existing "inherit the response context's identity"
// rule (CONTEXT.md D13) fills it in per-request, same as the embedded
// demo catalog does when it omits one.
//
// Known gap, not fixed here: the dashboard API's catalog detail response
// has no provider.availableAt (geo/address) — only providerId/providerName
// — so catalogs sourced this way can never satisfy a spatial Intent (M3).
func toCatalog(detail catalogDetailResponse) beckn.Catalog {
	resources := make([]beckn.Resource, 0, len(detail.Resources))
	for _, r := range detail.Resources {
		resources = append(resources, toResource(r))
	}

	offers := make([]beckn.Offer, 0, len(detail.Offers))
	for _, o := range detail.Offers {
		offers = append(offers, toOffer(o))
	}

	cat := beckn.Catalog{
		ID: detail.Catalog.ID,
		Descriptor: beckn.Descriptor{
			Name:      detail.Catalog.Name,
			ShortDesc: detail.Catalog.ShortDesc,
		},
		Provider: beckn.Provider{
			ID:         detail.Catalog.ProviderID,
			Descriptor: beckn.Descriptor{Name: detail.Catalog.ProviderName},
		},
		Resources: resources,
		Offers:    offers,
	}
	if detail.Catalog.ValidityStart != nil && detail.Catalog.ValidityEnd != nil {
		cat.Validity = &beckn.TimePeriod{
			StartDate: *detail.Catalog.ValidityStart,
			EndDate:   *detail.Catalog.ValidityEnd,
		}
	}
	return cat
}

func toResource(r resourceItem) beckn.Resource {
	res := beckn.Resource{
		ID: r.ID,
		Descriptor: beckn.Descriptor{
			Name:      r.Name,
			ShortDesc: r.ShortDesc,
			MediaFile: unmarshalMediaFiles(r.MediaFiles),
		},
		ResourceAttributes: unmarshalAttributes(r.ResourceAttributes),
	}
	return res
}

func toOffer(o offerItem) beckn.Offer {
	offer := beckn.Offer{
		ID: o.ID,
		Descriptor: beckn.Descriptor{
			Name:      o.Name,
			ShortDesc: o.ShortDesc,
		},
		ResourceIDs: o.ResourceIDs,
	}
	if o.ValidityStart != nil && o.ValidityEnd != nil {
		offer.Validity = &beckn.TimePeriod{
			StartDate: *o.ValidityStart,
			EndDate:   *o.ValidityEnd,
		}
	}
	// The dashboard API's catalog detail response carries at most one
	// consideration per offer (its query is `LIMIT 1`, meant for a UI
	// price column, not the full Consideration list a real publish
	// payload would have) and doesn't expose a consideration id/status —
	// synthesize both so the shape stays spec-valid.
	if attrs := unmarshalAttributes(o.ConsiderationAttributes); attrs != nil {
		offer.Considerations = []beckn.Consideration{{
			ID:                      o.ID + "-consideration",
			Status:                  beckn.Descriptor{Name: "active"},
			ConsiderationAttributes: attrs,
		}}
	}
	return offer
}

func unmarshalAttributes(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

func unmarshalMediaFiles(raw json.RawMessage) []beckn.MediaFile {
	if len(raw) == 0 {
		return nil
	}
	var files []beckn.MediaFile
	if err := json.Unmarshal(raw, &files); err != nil {
		return nil
	}
	return files
}

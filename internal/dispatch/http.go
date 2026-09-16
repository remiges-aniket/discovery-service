// Package dispatch contains adapters that deliver an on_discover callback
// to a BAP. HTTPDispatcher is the current (and, for now, only) adapter —
// it implements the service.OnDiscoverDispatcher port via a plain HTTP
// POST. A future adapter (e.g. one that queues and retries through a
// message broker) would live in its own file/package here and implement
// the same interface, with no change required in internal/service.
package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/remiges-tushar/discovery-service/internal/beckn"
	"github.com/remiges-tushar/discovery-service/internal/constants"
)

// RequestSigner signs an outbound request body, returning a
// beckn.Signature suitable for the Authorization header. See
// internal/signing.Signer for the concrete Ed25519 implementation.
type RequestSigner interface {
	Sign(body []byte) (beckn.Signature, error)
}

// HTTPDispatcher delivers on_discover callbacks over plain HTTP POST.
type HTTPDispatcher struct {
	client         *http.Client
	onDiscoverPath string
	signer         RequestSigner
}

// NewHTTPDispatcher builds an HTTPDispatcher. onDiscoverPath is appended
// to bapURI to form the callback URL — pass constants.OnDiscoverPath
// ("/on_discover") for the spec-correct default, or an overridden value
// (config.Config.OnDiscoverPath, from ON_DISCOVER_PATH in .env) when a
// real BAP's registered subscriber URL doesn't already include its full
// receiver route — see the Deliver doc comment and CONTEXT.md D14.
//
// signer, when non-nil, signs every outbound request body and sets the
// result as the Authorization header — required by real BAPs (observed:
// AUT_SIGNATURE_MISSING without it). Pass nil to send unsigned requests
// (e.g. in tests, or before a signing key is configured at all).
func NewHTTPDispatcher(client *http.Client, onDiscoverPath string, signer RequestSigner) *HTTPDispatcher {
	if client == nil {
		client = http.DefaultClient
	}
	return &HTTPDispatcher{client: client, onDiscoverPath: onDiscoverPath, signer: signer}
}

// Deliver POSTs payload as JSON to bapURI + d.onDiscoverPath.
//
// Per beckn.yaml, context.bapUri is supposed to already BE the BAP's
// full registered subscriber base URL, so appending "/on_discover"
// (the spec-correct default) should always be correct. In practice, a
// real observed BAP sandbox registered an incomplete bapUri that omitted
// its actual receiver route prefix (ONIX's bapTxnReceiver plugin exposes
// it under "/bap/receiver/", so the real path was
// "/bap/receiver/on_discover", not "/on_discover") — a config problem on
// that BAP's side, not something we should silently guess around by
// hardcoding a specific vendor's prefix. d.onDiscoverPath exists so a
// deployment can override the suffix to match what a specific
// counterpart actually expects, without touching code. The durable fix —
// resolving the real callback URL from a registry lookup instead of
// trusting/guessing at context.bapUri at all — is D3/M4 (auth) work; see
// CONTEXT.md §7's SSRF note, which is the same underlying gap.
//
// Known gap, see CONTEXT.md §7: this trusts bapURI as given by the caller
// rather than resolving it from a registry, which is an SSRF risk in
// production. Acceptable while auth (D3) is stubbed; must be fixed in M4.
func (d *HTTPDispatcher) Deliver(ctx context.Context, bapURI string, payload beckn.OnDiscoverRequest) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal on_discover payload: %w", err)
	}

	url := strings.TrimRight(bapURI, "/") + d.onDiscoverPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build on_discover request: %w", err)
	}
	req.Header.Set("Content-Type", constants.ContentTypeJSON)

	if d.signer != nil {
		sig, err := d.signer.Sign(body)
		if err != nil {
			return fmt.Errorf("sign on_discover request: %w", err)
		}
		req.Header.Set("Authorization", string(sig))
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("send on_discover to %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("on_discover callback to %s returned status %d", url, resp.StatusCode)
	}
	return nil
}

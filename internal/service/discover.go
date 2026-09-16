// Package service holds the discover business logic: request validation
// and building/delivering the on_discover result. It owns the interfaces
// ("ports") for every external component it depends on, so a concrete
// implementation (an "adapter", e.g. internal/dispatch) can be swapped
// without this package changing — see D8 in CONTEXT.md. The same pattern
// is how a future CatalogStore (Postgres now, ClickHouse or anything else
// later) will be introduced: an interface here, an adapter elsewhere.
package service

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/remiges-tech/logharbour/logharbour"
	"github.com/remiges-tushar/discovery-service/internal/beckn"
	"github.com/remiges-tushar/discovery-service/internal/constants"
)

// OnDiscoverDispatcher is the port for delivering an on_discover callback
// to a BAP. internal/dispatch.HTTPDispatcher is the current adapter; a
// future queued/retrying dispatcher could implement the same interface
// without any change here.
type OnDiscoverDispatcher interface {
	Deliver(ctx context.Context, bapURI string, payload beckn.OnDiscoverRequest) error
}

// AsyncRunner is the port for running a background job outside the
// request/response cycle. internal/worker.Pool is the current adapter —
// a bounded goroutine pool with panic recovery and graceful drain. Using
// an interface here (rather than calling `go func(){...}()` directly)
// means DiscoverService itself never spawns unbounded, unrecoverable
// goroutines; that responsibility is fully owned by whatever AsyncRunner
// is injected, and is swappable (e.g. for a queue-backed runner later)
// without touching this package. See CONTEXT.md D8/D9.
type AsyncRunner interface {
	// Run schedules fn to execute asynchronously and returns false
	// immediately (without running fn) if it cannot be scheduled right
	// now (e.g. the runner is at capacity) — it must never block the
	// caller.
	Run(fn func(ctx context.Context)) bool
}

// CatalogStore is the port for reading currently-known catalog data — the
// embedded M1 demo catalog (EmbeddedCatalogStore) by default, or real data
// polled from configured BPP sources (internal/catalogsource.Store) — see
// CONTEXT.md D17. Catalogs must never block on I/O: a store backed by a
// remote source is expected to poll/refresh in the background and serve
// its last-known-good snapshot here, not fetch synchronously per call.
type CatalogStore interface {
	Catalogs() []beckn.Catalog
}

// DiscoverService contains the /discover business logic, independent of
// HTTP transport (see internal/handlers), the delivery mechanism (see
// internal/dispatch), and how background work is scheduled (see
// internal/worker).
type DiscoverService struct {
	dispatcher      OnDiscoverDispatcher
	runner          AsyncRunner
	deliveryTimeout time.Duration
	logger          *logharbour.Logger
	// ownBppID/ownBppURI are this service's own registered identity,
	// stamped onto the on_discover context/catalogs when the incoming
	// request doesn't already address a specific BPP — see
	// BuildOnDiscover and config.Config.BppID/BppURI (CONTEXT.md D13).
	ownBppID  string
	ownBppURI string
	// catalogStore supplies the catalog data served on both the sync and
	// async /discover paths — see CatalogStore and CONTEXT.md D17.
	catalogStore CatalogStore
	// matchTimeout bounds how long a single request's intent matching
	// (matchCatalogs) may run before it's cut short — see
	// config.Config.MatchTimeout and CONTEXT.md D20.
	matchTimeout time.Duration
}

func NewDiscoverService(dispatcher OnDiscoverDispatcher, runner AsyncRunner, deliveryTimeout time.Duration, logger *logharbour.Logger, ownBppID, ownBppURI string, catalogStore CatalogStore, matchTimeout time.Duration) *DiscoverService {
	return &DiscoverService{
		dispatcher:      dispatcher,
		runner:          runner,
		deliveryTimeout: deliveryTimeout,
		logger:          logger,
		ownBppID:        ownBppID,
		ownBppURI:       ownBppURI,
		catalogStore:    catalogStore,
		matchTimeout:    matchTimeout,
	}
}

// Validate checks the minimum fields the spec marks required on the async
// POST /discover path: context.transactionId, context.messageId, and
// context.bapUri (bapUri is required here because it's where the
// on_discover callback is delivered). Full JSON-schema validation (against
// beckn.yaml) is a later milestone.
func (s *DiscoverService) Validate(req beckn.DiscoverRequest) (errCode, errMsg string) {
	if req.Context.TransactionID == "" {
		return constants.ErrRequiredFieldMissing, "context.transactionId is required"
	}
	if req.Context.MessageID == "" {
		return constants.ErrRequiredFieldMissing, "context.messageId is required"
	}
	if req.Context.BapURI == "" {
		return constants.ErrRequiredFieldMissing, "context.bapUri is required"
	}
	return validateIntent(req.Message.Intent)
}

// ValidateSync checks the fields required on the synchronous GET /discover
// path (see CONTEXT.md D16). Unlike Validate, it does NOT require
// context.bapUri: a synchronous caller gets the on_discover result back
// directly in the HTTP response body, so there is no callback to deliver
// and nothing for bapUri to address.
func (s *DiscoverService) ValidateSync(req beckn.DiscoverRequest) (errCode, errMsg string) {
	if req.Context.TransactionID == "" {
		return constants.ErrRequiredFieldMissing, "context.transactionId is required"
	}
	if req.Context.MessageID == "" {
		return constants.ErrRequiredFieldMissing, "context.messageId is required"
	}
	return validateIntent(req.Message.Intent)
}

// validateIntent rejects a message.intent this service cannot safely
// evaluate — currently only a malformed filters (see internal/service/match.go,
// CONTEXT.md's intent-matching decision entry). textSearch/spatial are
// never hard-rejected here: unsupported spatial operators are handled
// permissively at match time (matchesSpatial), not as a validation error.
// Called from both Validate and ValidateSync so a bad filters expression is
// a 400 before the request is ever Acked — the async POST /discover path
// Acks before any catalog matching runs, so this can't be caught later.
func validateIntent(intent beckn.Intent) (errCode, errMsg string) {
	if intent.Filters == nil {
		return "", ""
	}
	if intent.Filters.Type != "jsonpath" {
		return constants.ErrInvalidIntent, fmt.Sprintf("message.intent.filters.type must be \"jsonpath\", got %q", intent.Filters.Type)
	}
	if _, err := parseJSONPathFilter(intent.Filters.Expression); err != nil {
		return constants.ErrInvalidIntent, fmt.Sprintf("message.intent.filters.expression is not a valid JSONPath expression: %v", err)
	}
	return "", ""
}

// ProcessAsync schedules the on_discover result to be built and delivered
// in the background via s.runner. The caller (internal/handlers) has
// already sent the Ack before calling this.
//
// Two distinct failure modes are handled and logged differently:
//   - the runner has no capacity right now (queue full) — nothing ran, we
//     never even attempted delivery. This is backpressure, not a delivery
//     failure, so it's logged as a warning.
//   - the runner did run the job, but the HTTP delivery itself failed
//     (network error, non-2xx, timeout) — logged as an error.
//
// Neither is surfaced to the original /discover caller, who already has
// its Ack; retries/DLQ handling is a later milestone.
func (s *DiscoverService) ProcessAsync(req beckn.DiscoverRequest) {
	s.logger.LogActivity("on_discover dispatch scheduled", map[string]any{
		"transactionId": req.Context.TransactionID,
		"messageId":     req.Context.MessageID,
		"bapUri":        req.Context.BapURI,
	})

	accepted := s.runner.Run(func(ctx context.Context) {
		matchCtx, matchCancel := context.WithTimeout(ctx, s.matchTimeout)
		payload := BuildOnDiscover(matchCtx, req.Context, req.Message.Intent, s.ownBppID, s.ownBppURI, s.catalogStore.Catalogs())
		if matchCtx.Err() == context.DeadlineExceeded {
			s.logger.Warn().LogActivity("intent matching timed out, returning partial results", map[string]any{
				"transactionId": req.Context.TransactionID,
				"messageId":     req.Context.MessageID,
			})
		}
		matchCancel()

		deliverCtx, cancel := context.WithTimeout(ctx, s.deliveryTimeout)
		defer cancel()

		if err := s.dispatcher.Deliver(deliverCtx, req.Context.BapURI, payload); err != nil {
			s.logger.Err().LogActivity("on_discover delivery failed", map[string]any{
				"transactionId": req.Context.TransactionID,
				"messageId":     req.Context.MessageID,
				"bapUri":        req.Context.BapURI,
				"error":         err.Error(),
			})
			return
		}
		s.logger.LogActivity("on_discover delivered", map[string]any{
			"transactionId": req.Context.TransactionID,
			"messageId":     req.Context.MessageID,
			"bapUri":        req.Context.BapURI,
		})
	})
	if !accepted {
		s.logger.Warn().LogActivity("on_discover dispatch dropped: background runner at capacity", map[string]any{
			"transactionId": req.Context.TransactionID,
			"messageId":     req.Context.MessageID,
			"bapUri":        req.Context.BapURI,
		})
	}
}

// BuildSync builds and returns the on_discover result directly, for the
// synchronous GET /discover path (see CONTEXT.md D16 — mirrors
// beckn-discovr's GET /discover shortcut, which a real ION/BAP caller
// requires). Unlike ProcessAsync, nothing is dispatched over HTTP here:
// the caller (internal/handlers) writes the returned payload straight
// into its own response body.
func (s *DiscoverService) BuildSync(ctx context.Context, req beckn.DiscoverRequest) beckn.OnDiscoverRequest {
	s.logger.LogActivity("discover request served synchronously", map[string]any{
		"transactionId": req.Context.TransactionID,
		"messageId":     req.Context.MessageID,
	})

	matchCtx, cancel := context.WithTimeout(ctx, s.matchTimeout)
	defer cancel()

	resp := BuildOnDiscover(matchCtx, req.Context, req.Message.Intent, s.ownBppID, s.ownBppURI, s.catalogStore.Catalogs())
	if matchCtx.Err() == context.DeadlineExceeded {
		s.logger.Warn().LogActivity("intent matching timed out, returning partial results", map[string]any{
			"transactionId": req.Context.TransactionID,
			"messageId":     req.Context.MessageID,
		})
	}
	return resp
}

// PlaceholderSignature stands in for a real Ed25519 CounterSignature until
// auth (see CONTEXT.md D3) lands. Never treat this as a real signature —
// it is structurally valid (matches beckn.yaml's Signature pattern, see
// beckn.FormatSignature) but cryptographically meaningless.
func PlaceholderSignature() beckn.Signature {
	now := time.Now().Unix()
	placeholderValue := base64.StdEncoding.EncodeToString([]byte("unsigned-placeholder"))
	return beckn.FormatSignature(
		"unsigned", "placeholder", "none",
		now, now+300,
		"(created) (expires) digest",
		placeholderValue,
	)
}

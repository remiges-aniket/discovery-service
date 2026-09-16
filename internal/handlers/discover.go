// Package handlers is the HTTP transport layer: decode/encode JSON,
// enforce request-size limits, translate service-layer results into HTTP
// status codes. No business logic lives here — see internal/service.
package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/remiges-tech/logharbour/logharbour"
	"github.com/remiges-tushar/discovery-service/internal/beckn"
	"github.com/remiges-tushar/discovery-service/internal/constants"
	"github.com/remiges-tushar/discovery-service/internal/service"
	"github.com/remiges-tushar/discovery-service/internal/utility"
)

// DiscoverHandler implements both the async POST /discover path
// (ServeHTTP) and the synchronous GET /discover path (ServeSync, see
// CONTEXT.md D16).
type DiscoverHandler struct {
	svc          *service.DiscoverService
	maxBodyBytes int64
	logger       *logharbour.Logger
}

// NewDiscoverHandler builds a DiscoverHandler. maxBodyBytes caps how much
// of a /discover request body will be read (see config.Config.MaxRequestBodyBytes).
// Without a cap, decoding an attacker- or bug-supplied unbounded body
// reads it entirely into memory before json.Decode ever gets a chance to
// reject it — a single request could exhaust process memory.
func NewDiscoverHandler(svc *service.DiscoverService, maxBodyBytes int64, logger *logharbour.Logger) *DiscoverHandler {
	return &DiscoverHandler{svc: svc, maxBodyBytes: maxBodyBytes, logger: logger}
}

// ServeHTTP implements the async POST /discover path: Ack immediately,
// deliver on_discover to context.bapUri in the background.
func (h *DiscoverHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := h.decodeAndValidate(w, r, h.svc.Validate)
	if !ok {
		return
	}

	h.logger.LogActivity("discover request accepted", map[string]any{
		"transactionId": req.Context.TransactionID,
		"messageId":     req.Context.MessageID,
		"bapId":         req.Context.BapID,
		"bapUri":        req.Context.BapURI,
	})

	writeAck(w)
	h.svc.ProcessAsync(req)
}

// ServeSync implements the synchronous GET /discover path (see
// CONTEXT.md D16): the full on_discover result is returned inline in the
// response body instead of via an async callback. Mirrors beckn-discovr's
// GET /discover, which a real ION/BAP caller requires.
func (h *DiscoverHandler) ServeSync(w http.ResponseWriter, r *http.Request) {
	req, ok := h.decodeAndValidate(w, r, h.svc.ValidateSync)
	if !ok {
		return
	}

	h.logger.LogActivity("discover request accepted (sync)", map[string]any{
		"transactionId": req.Context.TransactionID,
		"messageId":     req.Context.MessageID,
		"bapId":         req.Context.BapID,
	})

	utility.WriteJSON(w, http.StatusOK, h.svc.BuildSync(req))
}

// decodeAndValidate reads and decodes the request body (size-bounded per
// h.maxBodyBytes) and runs the given validation func, writing a NACK and
// returning ok=false on any failure. Shared by both the async and
// synchronous /discover paths, which differ only in which fields they
// require (see service.Validate vs. service.ValidateSync).
func (h *DiscoverHandler) decodeAndValidate(w http.ResponseWriter, r *http.Request, validate func(beckn.DiscoverRequest) (errCode, errMsg string)) (beckn.DiscoverRequest, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBodyBytes)

	var req beckn.DiscoverRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logger.Warn().LogActivity("discover request rejected: invalid JSON body", nil)
		writeNack(w, http.StatusBadRequest, constants.ErrInvalidJSON, "request body is not valid JSON (or exceeds the size limit)")
		return beckn.DiscoverRequest{}, false
	}

	// Debug0-level detail: the full parsed request, visible only when
	// LOG_LEVEL selects a debug tier (see internal/logging.IsDebugLevel).
	h.logger.Debug0().LogDebug("discover request parsed", req)

	if errCode, errMsg := validate(req); errCode != "" {
		h.logger.Warn().LogActivity("discover request rejected: validation failed", map[string]any{
			"transactionId": req.Context.TransactionID,
			"messageId":     req.Context.MessageID,
			"errorCode":     errCode,
		})
		writeNack(w, http.StatusBadRequest, errCode, errMsg)
		return beckn.DiscoverRequest{}, false
	}

	return req, true
}

func writeAck(w http.ResponseWriter) {
	utility.WriteJSON(w, http.StatusOK, beckn.Ack{
		Status:    constants.StatusACK,
		Signature: service.PlaceholderSignature(),
	})
}

func writeNack(w http.ResponseWriter, status int, errCode, errMsg string) {
	utility.WriteJSON(w, status, beckn.Ack{
		Status:    constants.StatusNACK,
		Signature: service.PlaceholderSignature(),
		Error:     &beckn.Error{ErrorCode: errCode, ErrorMessage: errMsg},
	})
}

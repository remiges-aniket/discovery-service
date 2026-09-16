// Package utility holds small, generic, dependency-free helper functions
// shared across layers (currently just JSON HTTP responses). Keep this
// package narrow — a specific new helper file per concern is preferred
// over letting this become an unstructured grab-bag.
package utility

import (
	"encoding/json"
	"net/http"

	"github.com/remiges-tushar/discovery-service/internal/constants"
)

// WriteJSON writes v as a JSON response body with the given status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", constants.ContentTypeJSON)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

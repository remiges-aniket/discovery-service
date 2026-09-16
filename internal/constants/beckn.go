// Package constants holds project-wide constant values — Beckn protocol
// action/status/error-code strings, HTTP header values, and callback path
// suffixes — so they are defined once and never duplicated as string
// literals across handlers/service/dispatch.
//
// Named "constants" rather than "const": const is a reserved Go keyword and
// cannot be used as a package name.
package constants

const (
	// ActionDiscover and ActionOnDiscover are the Context.action values for
	// the two halves of the discover/on_discover exchange (beckn.yaml).
	ActionDiscover   = "discover"
	ActionOnDiscover = "on_discover"

	// StatusACK and StatusNACK are the Ack.status values.
	StatusACK  = "ACK"
	StatusNACK = "NACK"
)

// Beckn error codes (beckn-discovr's ErrorCodes.java convention — reused
// here since it already maps cleanly onto the v2 Error{errorCode,
// errorMessage} schema and there's no reason to invent a new taxonomy).
const (
	ErrInvalidJSON          = "SCH_INVALID_JSON"
	ErrRequiredFieldMissing = "SCH_REQUIRED_FIELD_MISSING"
)

const (
	ContentTypeJSON = "application/json"

	// OnDiscoverPath is appended to a BAP's bapUri to form the callback URL.
	OnDiscoverPath = "/on_discover"
)

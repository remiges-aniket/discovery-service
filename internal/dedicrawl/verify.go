package dedicrawl

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/gowebpki/jcs"
	"golang.org/x/crypto/blake2b"
)

// digestPrefix matches the exact format protocol-specifications-v2's
// Catalog_Publishing_and_Discovery.md specifies for file/index digests:
// "BLAKE2b-512=" + Base64(BLAKE2b-512(content)). This is a different
// literal prefix than internal/signing's "BLAKE-512=" (that package signs
// HTTP request/response bodies under a distinct signing scope — the v2
// message-authentication profile, not file-content digesting — so the two
// are deliberately not shared).
const digestPrefix = "BLAKE2b-512="

// CanonicalizeJCS marshals v to JSON and returns its RFC 8785 JSON
// Canonicalization Scheme (JCS) form — the byte sequence every
// detached signature in §10.1 is computed over, since ordinary
// json.Marshal output is not canonical (map key order, whitespace,
// number formatting all vary run to run).
func CanonicalizeJCS(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal for canonicalization: %w", err)
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return nil, fmt.Errorf("JCS transform: %w", err)
	}
	return canonical, nil
}

// Digest computes the spec's content digest over raw bytes.
func Digest(content []byte) string {
	sum := blake2b.Sum512(content)
	return digestPrefix + base64.StdEncoding.EncodeToString(sum[:])
}

// VerifyDigest reports an error if content's digest doesn't match want
// (as declared by an index entry's baseline/change/latest FileRef).
func VerifyDigest(content []byte, want string) error {
	got := Digest(content)
	if got != want {
		return fmt.Errorf("digest mismatch: want %s, got %s", want, got)
	}
	return nil
}

// signDetached signs canonicalBytes with priv and returns the base64
// Ed25519 signature — used only by StubRegistry/test fixtures to produce
// signatures VerifyDetached can check.
func signDetached(priv ed25519.PrivateKey, canonicalBytes []byte) string {
	sig := ed25519.Sign(priv, canonicalBytes)
	return base64.StdEncoding.EncodeToString(sig)
}

// VerifyDetached checks a base64 Ed25519 signature over canonicalBytes
// against pubKey. This is the file-level/index-entry-level signature
// scope (§10.1's "two new signature scopes"), independent of the HTTP
// message-signing profile in internal/signing.
func VerifyDetached(pubKey ed25519.PublicKey, canonicalBytes []byte, sigBase64 string) error {
	sig, err := base64.StdEncoding.DecodeString(sigBase64)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	if !ed25519.Verify(pubKey, canonicalBytes, sig) {
		return fmt.Errorf("signature verification failed")
	}
	return nil
}

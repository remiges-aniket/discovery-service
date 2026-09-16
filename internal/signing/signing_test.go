package signing

import (
	"crypto/ed25519"
	"encoding/base64"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/blake2b"
)

// specSignaturePattern mirrors beckn.yaml#/components/schemas/Signature
// (see internal/beckn/signature_test.go for the canonical citation).
var specSignaturePattern = regexp.MustCompile(
	`^Signature keyId="[^|"]+\|[^|"]+\|[^"]+",algorithm="[^"]+",created="\d+",expires="\d+",headers="[^"]+",signature="[A-Za-z0-9+/]+=*"$`,
)

func TestGenerateKeyPair_ProducesAUsableEd25519Pair(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair() error = %v", err)
	}
	if len(kp.Private) != ed25519.PrivateKeySize {
		t.Errorf("Private key size = %d, want %d", len(kp.Private), ed25519.PrivateKeySize)
	}
	if len(kp.Public) != ed25519.PublicKeySize {
		t.Errorf("Public key size = %d, want %d", len(kp.Public), ed25519.PublicKeySize)
	}
}

func TestSign_ProducesASignatureMatchingSpecPattern(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair() error = %v", err)
	}
	signer := NewSigner(kp, "bpp.example.com", "key-1", 5*time.Minute)

	sig, err := signer.Sign([]byte(`{"hello":"world"}`))
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	if !specSignaturePattern.MatchString(string(sig)) {
		t.Fatalf("Sign() = %q, does not match the spec Signature pattern", sig)
	}
	if !strings.Contains(string(sig), `keyId="bpp.example.com|key-1|ed25519"`) {
		t.Errorf("Sign() = %q, missing expected keyId", sig)
	}
}

func TestSign_ProducesAVerifiableEd25519Signature(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair() error = %v", err)
	}
	signer := NewSigner(kp, "bpp.example.com", "key-1", 5*time.Minute)

	body := []byte(`{"context":{"action":"on_discover"}}`)
	sig, err := signer.Sign(body)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	fields := parseSignatureFields(t, string(sig))

	digest := blake2b.Sum512(body)
	wantDigest := "BLAKE-512=" + base64.StdEncoding.EncodeToString(digest[:])
	// The digest isn't a field of the Signature string itself (per spec
	// it's part of what got signed, not what's transmitted alongside) —
	// instead, verify the signature bytes against the reconstructed
	// signing string using created/expires we parsed out.
	signingString := "(created): " + fields["created"] + "\n(expires): " + fields["expires"] + "\ndigest: " + wantDigest

	sigBytes, err := base64.StdEncoding.DecodeString(fields["signature"])
	if err != nil {
		t.Fatalf("decode signature field: %v", err)
	}
	if !ed25519.Verify(kp.Public, []byte(signingString), sigBytes) {
		t.Fatal("ed25519.Verify failed: signature does not verify against the reconstructed signing string and public key")
	}
}

func TestLoadOrGenerateKeyPair_GeneratesEphemeralWhenSeedEmpty(t *testing.T) {
	kp, ephemeral, err := LoadOrGenerateKeyPair("")
	if err != nil {
		t.Fatalf("LoadOrGenerateKeyPair(\"\") error = %v", err)
	}
	if !ephemeral {
		t.Error("ephemeral = false, want true when no seed is given")
	}
	if len(kp.Private) != ed25519.PrivateKeySize {
		t.Errorf("Private key size = %d, want %d", len(kp.Private), ed25519.PrivateKeySize)
	}
}

func TestLoadOrGenerateKeyPair_LoadsPersistentKeyWhenSeedGiven(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate seed key: %v", err)
	}
	seed := priv.Seed()
	seedB64 := base64.StdEncoding.EncodeToString(seed)

	kp, ephemeral, err := LoadOrGenerateKeyPair(seedB64)
	if err != nil {
		t.Fatalf("LoadOrGenerateKeyPair(seed) error = %v", err)
	}
	if ephemeral {
		t.Error("ephemeral = true, want false when a seed is given")
	}
	if !kp.Private.Equal(priv) {
		t.Error("loaded private key does not match the original seed's key")
	}
}

func TestSign_ExpiresIsCreatedPlusValidity(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair() error = %v", err)
	}
	signer := NewSigner(kp, "bpp.example.com", "key-1", 5*time.Minute)

	sig, err := signer.Sign([]byte("x"))
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	fields := parseSignatureFields(t, string(sig))

	created, err := strconv.ParseInt(fields["created"], 10, 64)
	if err != nil {
		t.Fatalf("parse created: %v", err)
	}
	expires, err := strconv.ParseInt(fields["expires"], 10, 64)
	if err != nil {
		t.Fatalf("parse expires: %v", err)
	}
	if got, want := expires-created, int64(300); got != want {
		t.Errorf("expires-created = %d, want %d (5 minutes)", got, want)
	}
}

// parseSignatureFields extracts keyId=".."-style fields from a
// Signature string for test assertions.
func parseSignatureFields(t *testing.T, sig string) map[string]string {
	t.Helper()
	fields := map[string]string{}
	re := regexp.MustCompile(`(\w+)="([^"]*)"`)
	for _, m := range re.FindAllStringSubmatch(sig, -1) {
		fields[m[1]] = m[2]
	}
	return fields
}

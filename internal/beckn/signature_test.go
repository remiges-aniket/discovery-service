package beckn

import (
	"regexp"
	"testing"
)

// specSignaturePattern is the exact pattern from ion-specs
// schema/core/v2/api/v2.0.0/beckn.yaml#/components/schemas/Signature.
// Signature (and therefore CounterSignature, which is `allOf: [Signature]`)
// is a STRING matching this pattern — not a JSON object. This test is the
// spec-compliance guardrail: if FormatSignature's output ever stops
// matching this, every Ack we send is malformed per the ION/Beckn v2
// contract.
var specSignaturePattern = regexp.MustCompile(
	`^Signature keyId="[^|"]+\|[^|"]+\|[^"]+",algorithm="[^"]+",created="\d+",expires="\d+",headers="[^"]+",signature="[A-Za-z0-9+/]+=*"$`,
)

func TestFormatSignature_MatchesSpecPattern(t *testing.T) {
	sig := FormatSignature("bpp.example.com", "key-1", "ed25519", 1735900000, 1735900300, "(created) (expires) digest", "AbC123+/==")

	if !specSignaturePattern.MatchString(string(sig)) {
		t.Fatalf("FormatSignature output does not match the spec pattern.\ngot:  %s\nwant: match %s", sig, specSignaturePattern)
	}
}

func TestFormatSignature_KeyIdIsSubscriberPipeUniqueKeyPipeAlgorithm(t *testing.T) {
	sig := FormatSignature("bpp.example.com", "key-1", "ed25519", 1, 2, "h", "c2ln")
	want := `Signature keyId="bpp.example.com|key-1|ed25519",algorithm="ed25519",created="1",expires="2",headers="h",signature="c2ln"`
	if string(sig) != want {
		t.Fatalf("FormatSignature() =\n%s\nwant:\n%s", sig, want)
	}
}

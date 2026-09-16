package dedicrawl

import "testing"

func TestCanonicalizeJCS_IsDeterministicRegardlessOfFieldOrder(t *testing.T) {
	type pair struct {
		B int `json:"b"`
		A int `json:"a"`
	}
	got1, err := CanonicalizeJCS(pair{B: 2, A: 1})
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	got2, err := CanonicalizeJCS(map[string]int{"b": 2, "a": 1})
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	if string(got1) != string(got2) {
		t.Fatalf("canonical forms differ: %q vs %q", got1, got2)
	}
	if string(got1) != `{"a":1,"b":2}` {
		t.Fatalf("unexpected canonical form: %q", got1)
	}
}

func TestVerifyDetached_AcceptsValidSignatureRejectsTampered(t *testing.T) {
	priv, pub := mustKeyPair(t)
	content := []byte(`{"a":1}`)
	sig := signDetached(priv, content)

	if err := VerifyDetached(pub, content, sig); err != nil {
		t.Fatalf("expected valid signature to verify, got %v", err)
	}

	if err := VerifyDetached(pub, []byte(`{"a":2}`), sig); err == nil {
		t.Fatal("expected tampered content to fail verification")
	}

	_, otherPub := mustKeyPair(t)
	if err := VerifyDetached(otherPub, content, sig); err == nil {
		t.Fatal("expected signature from a different key to fail verification")
	}
}

func TestVerifyDigest_DetectsMismatch(t *testing.T) {
	content := []byte("hello world")
	want := Digest(content)

	if err := VerifyDigest(content, want); err != nil {
		t.Fatalf("expected matching digest to verify, got %v", err)
	}
	if err := VerifyDigest([]byte("tampered"), want); err == nil {
		t.Fatal("expected mismatched content to fail digest verification")
	}
}

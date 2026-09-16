package service

import (
	"context"
	"io"
	"regexp"
	"testing"
	"time"

	"github.com/remiges-tech/logharbour/logharbour"
	"github.com/remiges-tushar/discovery-service/internal/beckn"
	"github.com/remiges-tushar/discovery-service/internal/worker"
)

// specSignaturePattern mirrors beckn.yaml#/components/schemas/Signature
// (see internal/beckn/signature_test.go for the canonical copy/citation).
var specSignaturePattern = regexp.MustCompile(
	`^Signature keyId="[^|"]+\|[^|"]+\|[^"]+",algorithm="[^"]+",created="\d+",expires="\d+",headers="[^"]+",signature="[A-Za-z0-9+/]+=*"$`,
)

func TestPlaceholderSignature_MatchesSpecPattern(t *testing.T) {
	sig := PlaceholderSignature()
	if !specSignaturePattern.MatchString(string(sig)) {
		t.Fatalf("PlaceholderSignature() = %q, does not match beckn.yaml's Signature pattern", sig)
	}
}

func testLogger() *logharbour.Logger {
	ctx := logharbour.NewLoggerContext(logharbour.Info)
	return logharbour.NewLoggerWithFallback(ctx, "service-test", logharbour.NewFallbackWriter(io.Discard, io.Discard))
}

// newTestRunner wires the real worker.Pool adapter rather than a fake, so
// these tests exercise the actual AsyncRunner implementation the service
// runs in production, not a stand-in that could hide integration bugs.
func newTestRunner(t *testing.T) *worker.Pool {
	p := worker.NewPool(2, 4, testLogger())
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	return p
}

type fakeDispatcher struct {
	delivered chan beckn.OnDiscoverRequest
	err       error
}

func newFakeDispatcher() *fakeDispatcher {
	return &fakeDispatcher{delivered: make(chan beckn.OnDiscoverRequest, 1)}
}

func (f *fakeDispatcher) Deliver(_ context.Context, _ string, payload beckn.OnDiscoverRequest) error {
	f.delivered <- payload
	return f.err
}

func TestValidate_RejectsMissingRequiredFields(t *testing.T) {
	svc := NewDiscoverService(newFakeDispatcher(), newTestRunner(t), time.Second, testLogger(), "own-bpp.example.com", "https://own-bpp.example.com", EmbeddedCatalogStore{})

	cases := []struct {
		name string
		req  beckn.DiscoverRequest
	}{
		{"missing transactionId", beckn.DiscoverRequest{Context: beckn.Context{MessageID: "m", BapURI: "https://bap"}}},
		{"missing messageId", beckn.DiscoverRequest{Context: beckn.Context{TransactionID: "t", BapURI: "https://bap"}}},
		{"missing bapUri", beckn.DiscoverRequest{Context: beckn.Context{TransactionID: "t", MessageID: "m"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errCode, errMsg := svc.Validate(tc.req)
			if errCode == "" {
				t.Fatalf("expected a validation error, got none (msg=%q)", errMsg)
			}
		})
	}
}

func TestValidate_AcceptsRequestWithRequiredFields(t *testing.T) {
	svc := NewDiscoverService(newFakeDispatcher(), newTestRunner(t), time.Second, testLogger(), "own-bpp.example.com", "https://own-bpp.example.com", EmbeddedCatalogStore{})
	req := beckn.DiscoverRequest{Context: beckn.Context{TransactionID: "t", MessageID: "m", BapURI: "https://bap"}}

	if errCode, errMsg := svc.Validate(req); errCode != "" {
		t.Fatalf("unexpected validation error: %s / %s", errCode, errMsg)
	}
}

// ValidateSync backs the synchronous GET /discover path (CONTEXT.md D16)
// and must NOT require context.bapUri — a synchronous caller has no
// callback to address.
func TestValidateSync_DoesNotRequireBapUri(t *testing.T) {
	svc := NewDiscoverService(newFakeDispatcher(), newTestRunner(t), time.Second, testLogger(), "own-bpp.example.com", "https://own-bpp.example.com", EmbeddedCatalogStore{})
	req := beckn.DiscoverRequest{Context: beckn.Context{TransactionID: "t", MessageID: "m"}}

	if errCode, errMsg := svc.ValidateSync(req); errCode != "" {
		t.Fatalf("unexpected validation error: %s / %s", errCode, errMsg)
	}
}

func TestValidateSync_StillRequiresTransactionIDAndMessageID(t *testing.T) {
	svc := NewDiscoverService(newFakeDispatcher(), newTestRunner(t), time.Second, testLogger(), "own-bpp.example.com", "https://own-bpp.example.com", EmbeddedCatalogStore{})

	cases := []struct {
		name string
		req  beckn.DiscoverRequest
	}{
		{"missing transactionId", beckn.DiscoverRequest{Context: beckn.Context{MessageID: "m"}}},
		{"missing messageId", beckn.DiscoverRequest{Context: beckn.Context{TransactionID: "t"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if errCode, _ := svc.ValidateSync(tc.req); errCode == "" {
				t.Fatal("expected a validation error, got none")
			}
		})
	}
}

func TestBuildSync_ReturnsOnDiscoverPayloadDirectly(t *testing.T) {
	svc := NewDiscoverService(newFakeDispatcher(), newTestRunner(t), time.Second, testLogger(), "own-bpp.example.com", "https://own-bpp.example.com", EmbeddedCatalogStore{})
	req := beckn.DiscoverRequest{Context: beckn.Context{TransactionID: "t1", MessageID: "m1"}}

	got := svc.BuildSync(req)

	if got.Context.Action != "on_discover" {
		t.Errorf("context.action = %q, want on_discover", got.Context.Action)
	}
	if got.Context.TransactionID != "t1" {
		t.Errorf("transactionId = %q, want t1", got.Context.TransactionID)
	}
	if len(got.Message.Catalogs) == 0 {
		t.Error("expected at least one catalog")
	}
}

func TestProcessAsync_DeliversFixedOnDiscoverPayload(t *testing.T) {
	dispatcher := newFakeDispatcher()
	svc := NewDiscoverService(dispatcher, newTestRunner(t), time.Second, testLogger(), "own-bpp.example.com", "https://own-bpp.example.com", EmbeddedCatalogStore{})
	req := beckn.DiscoverRequest{Context: beckn.Context{TransactionID: "t1", MessageID: "m1", BapURI: "https://bap.example.com"}}

	svc.ProcessAsync(req)

	select {
	case got := <-dispatcher.delivered:
		if got.Context.Action != "on_discover" {
			t.Errorf("Action = %q, want on_discover", got.Context.Action)
		}
		if len(got.Message.Catalogs) == 0 {
			t.Error("expected at least one catalog delivered")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ProcessAsync to call dispatcher.Deliver")
	}
}

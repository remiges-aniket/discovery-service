package catalogsource

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStore_Refresh_PopulatesFromSource(t *testing.T) {
	srv := newBPPStub(t, []string{"cat-1"}, sampleDetail)
	store := NewStore(NewHTTPSource([]string{srv.URL}, srv.Client(), testLogger()), time.Hour, testLogger())

	if got := store.Catalogs(); len(got) != 0 {
		t.Fatalf("expected no catalogs before the first Refresh, got %d", len(got))
	}

	store.Refresh(t.Context())

	got := store.Catalogs()
	if len(got) != 1 || got[0].ID != "cat-1" {
		t.Fatalf("Catalogs() after Refresh = %+v, want one catalog cat-1", got)
	}
}

func TestStore_Refresh_KeepsStaleDataWhenAllSourcesFail(t *testing.T) {
	srv := newBPPStub(t, []string{"cat-1"}, sampleDetail)
	store := NewStore(NewHTTPSource([]string{srv.URL}, srv.Client(), testLogger()), time.Hour, testLogger())
	store.Refresh(t.Context())
	if len(store.Catalogs()) != 1 {
		t.Fatal("setup: expected the first refresh to populate one catalog")
	}

	// Point the store at an unreachable source and refresh again — the
	// previously fetched catalog must survive, not be wiped to empty.
	store.source = NewHTTPSource([]string{"http://127.0.0.1:1"}, srv.Client(), testLogger())
	store.Refresh(t.Context())

	got := store.Catalogs()
	if len(got) != 1 || got[0].ID != "cat-1" {
		t.Fatalf("Catalogs() after a failed refresh = %+v, want the stale cat-1 to survive", got)
	}
}

func TestStore_Catalogs_ReturnsACopyNotSharedState(t *testing.T) {
	srv := newBPPStub(t, []string{"cat-1"}, sampleDetail)
	store := NewStore(NewHTTPSource([]string{srv.URL}, srv.Client(), testLogger()), time.Hour, testLogger())
	store.Refresh(t.Context())

	first := store.Catalogs()
	first[0].ID = "mutated"

	second := store.Catalogs()
	if second[0].ID != "cat-1" {
		t.Fatalf("mutating a returned slice affected the store's internal state: %+v", second)
	}
}

func TestStore_Start_RefreshesPeriodicallyUntilContextCancelled(t *testing.T) {
	var callCount int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/catalogs", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		_ = json.NewEncoder(w).Encode(catalogListResponse{Items: nil, Total: 0, Page: 1, Limit: listPageSize})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	store := NewStore(NewHTTPSource([]string{srv.URL}, srv.Client(), testLogger()), 10*time.Millisecond, testLogger())

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		store.Start(ctx)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after context cancellation")
	}

	if callCount == 0 {
		t.Error("expected at least one periodic refresh to have run")
	}
}

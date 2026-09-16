package worker

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/remiges-tech/logharbour/logharbour"
)

func testLogger() *logharbour.Logger {
	ctx := logharbour.NewLoggerContext(logharbour.Info)
	return logharbour.NewLoggerWithFallback(ctx, "worker-test", logharbour.NewFallbackWriter(io.Discard, io.Discard))
}

func TestPool_Run_ExecutesJobOnWorkerGoroutine(t *testing.T) {
	p := NewPool(2, 4, testLogger())
	defer func() { _ = p.Shutdown(context.Background()) }()

	done := make(chan struct{})
	if ok := p.Run(func(ctx context.Context) { close(done) }); !ok {
		t.Fatal("Run() = false, want true (queue not full)")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("job never ran")
	}
}

// A panic inside one job must not crash the process or the pool — this is
// the classic Go footgun: an unrecovered panic in a goroutine you didn't
// spawn yourself (e.g. deep in a "go func(){...}()") takes down the whole
// program, not just that goroutine.
func TestPool_Run_RecoversPanicAndKeepsProcessingSubsequentJobs(t *testing.T) {
	p := NewPool(1, 4, testLogger())
	defer func() { _ = p.Shutdown(context.Background()) }()

	p.Run(func(ctx context.Context) { panic("boom") })

	done := make(chan struct{})
	if ok := p.Run(func(ctx context.Context) { close(done) }); !ok {
		t.Fatal("Run() = false after a panicking job, want true — pool should still be alive")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pool stopped processing jobs after a panic — panic was not recovered per-job")
	}
}

func TestPool_Run_ReturnsFalseWithoutBlockingWhenQueueIsFull(t *testing.T) {
	// 1 worker permanently busy + queue capacity 1 => the 3rd Run must be
	// rejected immediately rather than blocking the caller (the caller is
	// an HTTP handler goroutine that already sent its response — it must
	// never be made to wait on background-job capacity).
	block := make(chan struct{})
	p := NewPool(1, 1, testLogger())
	defer func() {
		close(block)
		_ = p.Shutdown(context.Background())
	}()

	if !p.Run(func(ctx context.Context) { <-block }) {
		t.Fatal("first Run should be accepted (starts running immediately)")
	}
	// Give the worker a moment to actually pick up job 1 so the queue slot
	// used below is deterministically for job 2, not a race with job 1.
	time.Sleep(50 * time.Millisecond)

	if !p.Run(func(ctx context.Context) { <-block }) {
		t.Fatal("second Run should be accepted (fills the queue)")
	}

	resultCh := make(chan bool, 1)
	go func() { resultCh <- p.Run(func(ctx context.Context) {}) }()

	select {
	case ok := <-resultCh:
		if ok {
			t.Fatal("third Run should be rejected — queue is full")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Run blocked instead of returning false immediately when the queue is full")
	}
}

func TestPool_Shutdown_WaitsForInFlightJobsToComplete(t *testing.T) {
	p := NewPool(2, 4, testLogger())

	var finished atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	p.Run(func(ctx context.Context) {
		defer wg.Done()
		time.Sleep(200 * time.Millisecond)
		finished.Store(true)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	if !finished.Load() {
		t.Fatal("Shutdown returned before the in-flight job finished")
	}
}

func TestPool_Shutdown_ReturnsContextErrorWhenJobsExceedDeadline(t *testing.T) {
	p := NewPool(1, 4, testLogger())

	release := make(chan struct{})
	defer close(release)
	p.Run(func(ctx context.Context) { <-release })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := p.Shutdown(ctx); err == nil {
		t.Fatal("expected Shutdown to return a deadline error for a job that outlives the shutdown timeout")
	}
}

func TestPool_Run_AfterShutdown_ReturnsFalseWithoutPanicking(t *testing.T) {
	p := NewPool(1, 1, testLogger())
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	if ok := p.Run(func(ctx context.Context) {}); ok {
		t.Fatal("Run() after Shutdown should return false")
	}
}

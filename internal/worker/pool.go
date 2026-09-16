// Package worker provides a small bounded goroutine pool for running
// fire-and-forget background jobs (e.g. delivering an on_discover
// callback after a /discover handler has already responded).
//
// It exists to close three concrete Go concurrency defects that a naive
// `go func(){ ... }()` per request has:
//
//  1. Unbounded goroutine fan-out. A traffic burst would otherwise spawn
//     one goroutine (and, for our use case, one outbound HTTP connection)
//     per request with no ceiling — a resource-exhaustion vector. Pool
//     caps concurrency to a fixed worker count and a bounded queue.
//  2. Unrecovered panics. A panic inside a bare `go func(){ ... }()` that
//     is never recovered crashes the entire process — Go does not isolate
//     goroutine panics the way e.g. Erlang isolates process crashes.
//     Pool recovers per-job, so one bad job can't take the service down.
//  3. No graceful drain on shutdown. Killing the process while background
//     jobs are in flight silently drops them. Pool.Shutdown waits (up to
//     a caller-supplied deadline) for in-flight jobs to finish.
package worker

import (
	"context"
	"sync"

	"github.com/remiges-tech/logharbour/logharbour"
)

// Pool runs submitted jobs on a fixed set of worker goroutines.
type Pool struct {
	jobs   chan func(context.Context)
	wg     sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc
	logger *logharbour.Logger

	mu       sync.Mutex // guards stopping vs. concurrent Run/close of jobs
	stopping bool
}

// NewPool starts `workers` goroutines consuming from a queue of capacity
// `queueSize`. The pool must be stopped with Shutdown. logger is used
// solely to report a recovered panic from a job (see runJob) — pass a
// logharbour Logger built via internal/logging.New.
func NewPool(workers, queueSize int, logger *logharbour.Logger) *Pool {
	ctx, cancel := context.WithCancel(context.Background())
	p := &Pool{
		jobs:   make(chan func(context.Context), queueSize),
		ctx:    ctx,
		cancel: cancel,
		logger: logger,
	}
	for range workers {
		p.wg.Add(1)
		go p.runWorker()
	}
	return p
}

func (p *Pool) runWorker() {
	defer p.wg.Done()
	for {
		select {
		case fn, ok := <-p.jobs:
			if !ok {
				return
			}
			p.runJob(fn)
		case <-p.ctx.Done():
			return
		}
	}
}

func (p *Pool) runJob(fn func(context.Context)) {
	defer func() {
		if r := recover(); r != nil {
			p.logger.Err().LogActivity("worker: recovered panic in background job", map[string]any{
				"panic": r,
			})
		}
	}()
	fn(p.ctx)
}

// Run enqueues fn to execute on a worker goroutine, passing it a context
// tied to the pool's lifetime (cancelled when Shutdown's deadline expires
// before all jobs finish). It never blocks: if the pool is stopping or the
// queue is full, it returns false immediately and the caller decides what
// to do (the current /discover handler logs a warning and drops the
// delivery — see CONTEXT.md; a durable outbox/retry queue is future work).
func (p *Pool) Run(fn func(context.Context)) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopping {
		return false
	}
	select {
	case p.jobs <- fn:
		return true
	default:
		return false
	}
}

// Shutdown stops accepting new jobs and waits for in-flight and queued
// jobs to finish. If ctx is done first, it cancels the shared worker
// context and returns ctx.Err() immediately.
//
// It deliberately does NOT keep waiting for workers to actually exit in
// that case. Go cannot forcibly kill a goroutine — cancellation is only
// ever cooperative, via ctx. A job that ignores its ctx argument (or is
// blocked on something that doesn't) can run indefinitely; if Shutdown
// kept blocking on wg.Wait() until every worker noticed cancellation and
// returned, one uncooperative job would turn a caller-bounded "graceful
// shutdown with a deadline" into an unbounded hang — precisely defeating
// the reason a deadline was passed in. So: honor the deadline, accept that
// a stuck worker goroutine may leak past it, and let the process-level
// shutdown (e.g. the container/orchestrator's own kill timeout) be the
// real backstop. In practice this pool is only ever given jobs built on
// context-aware I/O (http.NewRequestWithContext etc.), so cancellation
// does propagate and this path is a safety net, not the common case.
func (p *Pool) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	p.stopping = true
	close(p.jobs)
	p.mu.Unlock()

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		p.cancel()
		return ctx.Err()
	}
}

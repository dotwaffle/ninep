package server

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// errTflushCancelled is the cause recorded when a Tflush aborts an inflight
// request. Err() still returns context.Canceled verbatim to preserve
// behavioral parity with the prior context.WithCancel implementation; this
// sentinel is reserved for a future context.Cause-aware caller (see
// RESEARCH OQ-2 / CONTEXT D-05).
var errTflushCancelled = errors.New("9p: request cancelled by Tflush")

// errConnCleanup is the cause recorded when connection cleanup (or
// Tversion-triggered drain) cancels all inflight requests. Same Err()
// semantics as errTflushCancelled; distinct identity for future callers.
var errConnCleanup = errors.New("9p: connection cleanup cancelled request")

// requestCtx is a pooled, lazy-cancel context.Context implementation used on
// the per-request hot path. It replaces the context.WithCancel allocation
// that formerly lived at conn.go's per-request setup. Created via
// getRequestCtx(parent); recycled via putRequestCtx after dispatchInline's
// defer chain completes.
//
// Concurrency: three goroutines can race on a single *requestCtx at once —
// (1) the handler calling ctx.Done()/ctx.Err(), (2) a Tflush-handling sibling
// calling flush(), (3) the dispatchInline defer calling putRequestCtx. The
// state machine is:
//
//   - flushed atomic.Bool: the sole truth for "has the request been
//     cancelled". Readable lock-free from Err().
//   - done chan struct{}: allocated lazily under initOnce only when a caller
//     first invokes Done(). Nil on the happy path where the handler never
//     blocks on cancellation.
//   - initOnce: guards done = make(chan struct{}) exactly once per lifecycle.
//   - closeOnce: guards close(done) exactly once. Two potential closers —
//     flush() (after winning the CAS on flushed) and Done()-after-flush
//     (lazy-init path sees flushed==true). closeOnce makes either ordering
//     safe. (RESEARCH OQ-3, Pitfall 3.)
//
// Lifecycle: getRequestCtx borrows from requestCtxPool; putRequestCtx resets
// ALL state (flushed, done, initOnce, closeOnce, parent) before returning it
// to the pool. Dirty state MUST NOT leak — a stale closed `done` would
// surface as phantom cancellation to the next borrower. (CONTEXT D-08,
// RESEARCH Pitfall 1.)
//
// Not safe for external retention — callers MUST NOT store the value or its
// Done() channel past the dispatchInline lifetime.
type requestCtx struct {
	parent    context.Context // for Value() delegation only; nil after put
	done      chan struct{}   // nil until Done() called; reset to nil on put
	initOnce  sync.Once       // guards done = make(chan struct{})
	closeOnce sync.Once       // guards close(done)
	flushed   atomic.Bool
}

// Deadline always returns no deadline. The per-request path does not use
// context.WithDeadline/WithTimeout today; adding support would complicate
// the pool design and is deferred (CONTEXT §Deferred).
func (r *requestCtx) Deadline() (time.Time, bool) { return time.Time{}, false }

// Done returns a channel that is closed when the request is flushed. The
// channel is lazily allocated on first call; if flush() has already fired
// before Done() is invoked, the freshly-allocated channel is closed
// immediately via closeOnce.
func (r *requestCtx) Done() <-chan struct{} {
	r.initOnce.Do(func() { r.done = make(chan struct{}) })
	if r.flushed.Load() {
		r.closeOnce.Do(func() { close(r.done) })
	}
	return r.done
}

// Err returns context.Canceled after flush() has been called, else nil.
// The hardcoded identity matches the prior context.WithCancel behaviour so
// existing callers doing errors.Is(err, context.Canceled) continue to work
// (RESEARCH OQ-2; conn_test.go:385-387 live check).
func (r *requestCtx) Err() error {
	if r.flushed.Load() {
		return context.Canceled
	}
	return nil
}

// Value delegates to the parent context. parent is nilled on pool return;
// callers must not invoke Value after putRequestCtx.
func (r *requestCtx) Value(key any) any { return r.parent.Value(key) }

// flush marks the request cancelled and wakes any caller parked on Done().
// Idempotent via a CAS on flushed; double-close of the done channel is
// prevented by closeOnce.
//
// The err argument is reserved for future context.Cause support; current
// Err() returns context.Canceled to preserve behavioural parity with the
// prior context.WithCancel implementation (CONTEXT D-05, RESEARCH OQ-2).
func (r *requestCtx) flush(_ error) {
	if !r.flushed.CompareAndSwap(false, true) {
		return
	}
	if r.done != nil {
		r.closeOnce.Do(func() { close(r.done) })
	}
}

// requestCtxPool backs getRequestCtx / putRequestCtx. Package-global per
// msgcache.go precedent; sync.Pool's per-P balancing is acceptable for this
// workload because each borrow is single-use per request (RESEARCH A1).
var requestCtxPool = sync.Pool{
	New: func() any { return &requestCtx{} },
}

// getRequestCtx borrows a *requestCtx from the pool and wires its parent.
// All other state is expected to already be zero — putRequestCtx is the
// sole mutator. Steady-state allocs: 0 (see TestRequestCtxAllocs / PERF-08).
func getRequestCtx(parent context.Context) *requestCtx {
	r := requestCtxPool.Get().(*requestCtx)
	r.parent = parent
	return r
}

// putRequestCtx resets ALL state and returns the *requestCtx to the pool.
// Leaving any field dirty WILL cause phantom cancellation on the next
// borrower (RESEARCH Pitfall 1; TestRequestCtxPoolReuseNoPhantomCancel is
// the regression guard).
func putRequestCtx(r *requestCtx) {
	r.flushed.Store(false)
	r.done = nil
	r.initOnce = sync.Once{}
	r.closeOnce = sync.Once{}
	r.parent = nil
	requestCtxPool.Put(r)
}

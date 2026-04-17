//go:build go1.25

package server

import (
	"context"
	"runtime"
	"testing"
	"testing/synctest"
)

// TestRequestCtxTflushCancel verifies that after flush() on a borrowed
// requestCtx, Done() returns a closed channel AND Err() returns
// context.Canceled — behavioral parity with the prior context.WithCancel
// implementation (CONTEXT D-05, RESEARCH OQ-2).
func TestRequestCtxTflushCancel(t *testing.T) {
	t.Parallel()

	r := getRequestCtx(context.Background())
	defer putRequestCtx(r)

	done := r.Done()
	select {
	case <-done:
		t.Fatal("Done() already closed before flush")
	default:
	}
	if err := r.Err(); err != nil {
		t.Fatalf("Err() before flush = %v, want nil", err)
	}

	r.flush(errTflushCancelled)

	select {
	case <-done:
		// expected
	default:
		t.Fatal("Done() not closed after flush")
	}
	if err := r.Err(); err != context.Canceled {
		t.Fatalf("Err() after flush = %v, want context.Canceled", err)
	}
}

// TestRequestCtxTflushCancelBeforeDone verifies the flush-then-Done ordering:
// if flush() runs before Done() is ever called, the next Done() call must
// return an already-closed channel. Guards Pitfall 2 (RESEARCH).
func TestRequestCtxTflushCancelBeforeDone(t *testing.T) {
	t.Parallel()

	r := getRequestCtx(context.Background())
	defer putRequestCtx(r)

	r.flush(errTflushCancelled)
	if err := r.Err(); err != context.Canceled {
		t.Fatalf("Err() after flush = %v, want context.Canceled", err)
	}

	done := r.Done()
	select {
	case <-done:
		// expected — Done() lazy-allocated and closed the channel because
		// flushed was already true.
	default:
		t.Fatal("Done() returned open channel after prior flush()")
	}
}

// TestRequestCtxPoolReuseNoPhantomCancel is the regression guard for D-08 /
// Pitfall 1. Request A borrows, observes Done(), is flushed, and returns to
// the pool. Request B borrows and MUST observe Err()==nil and a non-closed
// Done() channel. synctest gives deterministic scheduling.
func TestRequestCtxPoolReuseNoPhantomCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Request A: borrow, observe Done(), flush, return to pool.
		a := getRequestCtx(context.Background())
		doneA := a.Done()
		a.flush(errTflushCancelled)
		select {
		case <-doneA:
			// expected
		default:
			t.Fatal("A.Done() not closed after flush")
		}
		if err := a.Err(); err != context.Canceled {
			t.Fatalf("A.Err() = %v, want context.Canceled", err)
		}
		putRequestCtx(a)

		// Request B: borrow. Assert Err() == nil BEFORE calling Done() so
		// a pool-state leak via flushed==true fails the test here, not
		// indirectly via a pre-closed channel.
		b := getRequestCtx(context.Background())
		defer putRequestCtx(b)
		if err := b.Err(); err != nil {
			t.Fatalf("B.Err() = %v, want nil (phantom cancel from A state)", err)
		}
		doneB := b.Done()
		select {
		case <-doneB:
			t.Fatal("B.Done() closed without flush (phantom cancellation)")
		default:
			// expected
		}
	})
}

// TestRequestCtxFlushIdempotent asserts flush() is idempotent and the
// closeOnce guard prevents double-close panic. Covers Pitfall 3 (RESEARCH).
func TestRequestCtxFlushIdempotent(t *testing.T) {
	t.Parallel()

	r := getRequestCtx(context.Background())
	defer putRequestCtx(r)

	done := r.Done()
	r.flush(errTflushCancelled)
	r.flush(errConnCleanup) // second call must not panic.

	if err := r.Err(); err != context.Canceled {
		t.Fatalf("Err() after double flush = %v, want context.Canceled", err)
	}
	// Receive from closed channel returns zero value, non-blocking — two
	// recv ops both succeed on a channel closed exactly once.
	select {
	case <-done:
	default:
		t.Fatal("Done() not closed after flush")
	}
}

// TestRequestCtxAllocs asserts 0 allocs/op at steady state (pool hit) and
// ≤1 alloc/op on cold pool (pool miss). PERF-08.2.
func TestRequestCtxAllocs(t *testing.T) {
	// Do not run t.Parallel — testing.AllocsPerRun demands a steady
	// allocator and parallel tests may perturb the sync.Pool.

	parent := context.Background()

	// Warm the pool so AllocsPerRun measures the steady-state hit path.
	for range 32 {
		putRequestCtx(getRequestCtx(parent))
	}

	got := testing.AllocsPerRun(100, func() {
		r := getRequestCtx(parent)
		_ = r.Err()
		putRequestCtx(r)
	})
	t.Logf("steady-state allocs/op = %.2f", got)
	if got > 0 {
		t.Fatalf("steady-state allocs/op = %.2f, want 0", got)
	}

	// Cold-pool: force a GC to drain sync.Pool's per-P caches, then take
	// one measurement. sync.Pool drains at GC; this makes the next Get a
	// pool miss that allocates a fresh *requestCtx. Expect ≤1 alloc.
	runtime.GC()
	runtime.GC()
	cold := testing.AllocsPerRun(1, func() {
		r := getRequestCtx(parent)
		_ = r.Err()
		putRequestCtx(r)
	})
	t.Logf("cold-pool allocs/op = %.2f", cold)
	if cold > 1 {
		t.Fatalf("cold-pool allocs/op = %.2f, want ≤1", cold)
	}
}

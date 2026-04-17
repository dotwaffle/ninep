//go:build stress

package server

import (
	"context"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
)

// TestRequestCtxRaceTflush exercises the full race surface on *requestCtx:
// flush() vs Done() vs putRequestCtx contention across 1000 goroutines under
// -race. Complements the deterministic synctest regression test
// (TestRequestCtxPoolReuseNoPhantomCancel) by running real goroutines on real
// cores with the race detector active. Gated by //go:build stress so the
// default `go test -race -count=1 ./...` does not run it; CI has a dedicated
// job per CONTEXT D-11.
//
// Invariants verified:
//  1. No data races detected by the race runtime.
//  2. No panic (notably `close of closed channel` — RESEARCH Pitfall 3 / OQ-3).
//  3. shouldFlush==true goroutines, after joining the flush goroutine, MUST
//     observe Done() closed AND Err()==context.Canceled (post-flush state is
//     monotonic — once flush has returned, it stays flushed for this rctx).
//  4. shouldFlush==false goroutines MUST observe Done() open and Err()==nil
//     for the entire pre-put lifetime of the rctx (no concurrent flush exists
//     for this rctx).
//  5. After putRequestCtx + re-get, no phantom cancellation leaks through the
//     shared pool. Verified implicitly by invariant 4 across 1000 iterations:
//     any leak from a prior flushed rctx would surface a false-positive here
//     (e.g., a rctx that never had flush called against it observing Err()!=nil).
//
// Note on race-free checks: Err() and Done() must be sampled AFTER the flush
// goroutine has been joined (if any), to avoid a TOCTOU where flush fires
// between the Done() read and the Err() read. Sampling before flush.Wait()
// is inherently racy because flush is allowed to run concurrently.
func TestRequestCtxRaceTflush(t *testing.T) {
	t.Parallel()
	const N = 1000

	var errCount atomic.Int64
	var wg sync.WaitGroup
	wg.Add(N)

	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			rctx := getRequestCtx(context.Background())

			// 50% probability that a sibling goroutine fires flush concurrently.
			var flushWG sync.WaitGroup
			shouldFlush := rand.IntN(2) == 0
			if shouldFlush {
				flushWG.Add(1)
				go func() {
					defer flushWG.Done()
					rctx.flush(errTflushCancelled)
				}()
			}

			// Handler-side: touch Done() during the race window so the
			// flush-before-Done vs Done-before-flush ordering is exercised
			// under -race (the channel may still be open at this point if
			// shouldFlush==true and the sibling has not yet run).
			_ = rctx.Done()

			// Wait for the flush goroutine (if any) before asserting the
			// final state. After flushWG.Wait(), the state is monotonic:
			// either flush ran (shouldFlush==true) or flush never ran
			// (shouldFlush==false).
			flushWG.Wait()

			done := rctx.Done()
			if shouldFlush {
				select {
				case <-done:
					// expected
				default:
					errCount.Add(1)
					t.Errorf("shouldFlush=true: Done() open after flush join")
				}
				if rctx.Err() != context.Canceled {
					errCount.Add(1)
					t.Errorf("shouldFlush=true: Err() = %v, want context.Canceled", rctx.Err())
				}
			} else {
				select {
				case <-done:
					errCount.Add(1)
					t.Errorf("shouldFlush=false: Done() closed without flush (phantom cancel)")
				default:
					// expected
				}
				if rctx.Err() != nil {
					errCount.Add(1)
					t.Errorf("shouldFlush=false: Err() = %v, want nil (phantom cancel)", rctx.Err())
				}
			}

			putRequestCtx(rctx)
		}()
	}
	wg.Wait()

	if errCount.Load() > 0 {
		t.Fatalf("%d invariant violations detected (see Errorf output above)", errCount.Load())
	}
}

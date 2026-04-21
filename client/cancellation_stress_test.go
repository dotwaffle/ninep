package client_test

import (
	"context"
	"errors"
	"math/rand/v2"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
)

// TestClient_Cancellation_Stress exercises flushAndWait + close + ctx +
// deadline composition under -race with 500 concurrent goroutines × 10
// iterations = 5000 ops. Each iteration picks one of four op-modes:
//
//  0. complete — no cancel
//  1. cancel-immediately — opCancel() before issuing the op
//  2. cancel-mid-flight — async cancel after a ~µs-range sleep
//  3. deadline-expiry — WithTimeout short enough to fire in-flight
//
// A single observer goroutine fires cli.Close() mid-stream so the close
// path is exercised alongside cancellation. After the wait group drains,
// the test asserts:
//
//   - ≥1 context.Canceled observation (hard)
//   - ≥1 context.DeadlineExceeded observation (hard)
//   - ≥1 ErrClosed observation (hard)
//   - ErrFlushed is timing-dependent on net.Pipe — Logf-warned, not
//     hard-asserted (Research Q3 + Pitfall 4)
//   - runtime.NumGoroutine() delta ≤ 5 after cleanup
//   - no panics / no -race violations
//
// Runtime budget: 30s default per Research Q3; t.Short() skips the test.
// Satisfies ROADMAP Phase 22 SC-3 (500+ -race stress, no leaks, random
// cancellation + close). Runs in the default suite (no build tag),
// matching Phase 19's TestClient_Concurrent / TestClient_TagReuse_Stress
// precedent.
func TestClient_Cancellation_Stress(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("stress test skipped under -short")
	}

	const (
		numG  = 500
		iters = 10
		// Aggressive deadline — net.Pipe latency is ~µs, so a short
		// deadline reliably fires in-flight for mode 3.
		deadlineShort = 500 * time.Microsecond
	)

	// Capture baseline goroutine count BEFORE booting the pair so any
	// server/client goroutines are in the delta. Brief GC + sleep to
	// settle stragglers from earlier parallel tests.
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	// NOTE: don't defer cleanup — some goroutines WILL observe Close().
	// We invoke cleanup explicitly after wg.Wait().

	parent, cancelParent := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancelParent()

	// Attach root at fid=0. Raw's fid-taking form mirrors the
	// concurrent_test pattern; every stress goroutine derives its per-iter
	// fid from its id + iteration so there's no cross-goroutine collision.
	if _, err := cli.Raw().Attach(parent, proto.Fid(0), "me", ""); err != nil {
		cleanup()
		t.Fatalf("Attach: %v", err)
	}

	var observed cancellationStressCounters

	// Observer goroutine fires cli.Close() once a threshold of operations have started.
	// This ensures we always hit the close path even on very fast machines where
	// a hardcoded sleep would be too long.
	closeFired := make(chan struct{})
	startCount := atomic.Int32{}
	const closeThreshold = 2500 // Fire close halfway through

	var wg sync.WaitGroup
	for g := range numG {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(gid), uint64(gid)*0x9E3779B97F4A7C15))
			for i := range iters {
				if startCount.Add(1) == closeThreshold {
					go func() {
						_ = cli.Close()
						close(closeFired)
					}()
				}

				mode := r.IntN(4)
				opCtx, opCancel := context.WithCancel(parent)

				switch mode {
				case 0:
					// complete — no cancel
				case 1:
					// cancel-immediate
					opCancel()
				case 2:
					// cancel-mid-flight — async cancel after a random
					// sub-millisecond delay. Using time.AfterFunc to
					// avoid spawning a goroutine per iter (would dwarf
					// the stress test's own goroutine count).
					time.AfterFunc(
						time.Duration(r.IntN(200))*time.Microsecond,
						opCancel,
					)
				case 3:
					// deadline-expiry — stack WithTimeout on opCtx so
					// both opCancel and the dl cancel run cleanly.
					var dlCancel context.CancelFunc
					opCtx, dlCancel = context.WithTimeout(opCtx, deadlineShort)
					// Defer to ensure the timer is stopped even if the
					// op returns before the deadline fires.
					defer dlCancel()
				}

				err := issueStressOp(opCtx, cli, gid, i)
				opCancel()
				observed.classify(err)
			}
		}(g)
	}

	wg.Wait()

	// Ensure Close fully drained before checking the goroutine baseline.
	// If the observer never ran (unlikely — 500 × 10 ops far outlast 80ms
	// on -race) we still cleanup normally.
	select {
	case <-closeFired:
	case <-time.After(5 * time.Second):
		// Observer didn't run or Close didn't complete — force cleanup
		// so the test doesn't hang.
		t.Log("warning: closeFired not signalled within 5s; forcing cleanup")
	}
	// Ensure cleanup ran regardless of observer path. cleanup is
	// idempotent (Close + server cancel + server wait) so double-invoke
	// is safe.
	cleanup()

	// Drain scheduler so server/client goroutines actually exit before
	// the leak check. Mirrors TestClient_Close_GoroutineLeak's poll loop.
	for range 40 {
		runtime.GC()
		time.Sleep(50 * time.Millisecond)
		if runtime.NumGoroutine() <= baseline+5 {
			break
		}
	}

	final := runtime.NumGoroutine()
	if final > baseline+5 {
		t.Errorf("goroutine leak: baseline=%d, final=%d (delta=%d)",
			baseline, final, final-baseline)
	}

	c := observed.snapshot()
	t.Logf("observed: closed=%d flushed=%d canceled=%d deadline=%d completed=%d server_err=%d",
		c.closed, c.flushed, c.canceled, c.deadline, c.completed, c.serverErr)
	t.Logf("ops total: %d (goroutines=%d × iters=%d)", numG*iters, numG, iters)

	if c.closed == 0 {
		t.Error("expected ≥1 ErrClosed observation (close path not exercised)")
	}
	if c.canceled == 0 {
		t.Error("expected ≥1 context.Canceled observation (cancel path not exercised)")
	}
	if c.deadline == 0 {
		t.Error("expected ≥1 context.DeadlineExceeded observation (deadline path not exercised)")
	}
	// ErrFlushed is order-dependent on net.Pipe (synchronous; Rread often
	// lands before Rflush). Warn only, per Research Q3 + Pitfall 4.
	if c.flushed == 0 {
		t.Logf("warning: no ErrFlushed observations " +
			"(Rflush-first race did not fire on this run; not a failure)")
	}
}

// cancellationStressCounters tallies each sentinel outcome observed
// across the stress goroutines. Access is serialised by mu.
type cancellationStressCounters struct {
	mu        sync.Mutex
	closed    int
	flushed   int
	canceled  int
	deadline  int
	completed int
	serverErr int // server-reported *Error (ENOENT etc.) — round-trip succeeded
}

type cancellationStressSnapshot struct {
	closed    int
	flushed   int
	canceled  int
	deadline  int
	completed int
	serverErr int
}

func (c *cancellationStressCounters) classify(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case err == nil:
		c.completed++
	case errors.Is(err, client.ErrClosed):
		c.closed++
	case errors.Is(err, client.ErrFlushed):
		// Additive — ErrFlushed coexists with ctx.Err in the chain per
		// Plan 22-02 D-05. Count the flushed observation; also bump the
		// matching ctx counter so those assertions can fire on the
		// Rflush-first path even if mode-1/2 never hit the R-first path.
		c.flushed++
		switch {
		case errors.Is(err, context.Canceled):
			c.canceled++
		case errors.Is(err, context.DeadlineExceeded):
			c.deadline++
		}
	case errors.Is(err, context.Canceled):
		c.canceled++
	case errors.Is(err, context.DeadlineExceeded):
		c.deadline++
	default:
		// Server-reported *Error (e.g. ENOENT on the "nonexistent.txt"
		// branch). Wire round-trip succeeded; counts as a normal
		// completion for leak-detection purposes.
		c.serverErr++
	}
}

func (c *cancellationStressCounters) snapshot() cancellationStressSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cancellationStressSnapshot{
		closed:    c.closed,
		flushed:   c.flushed,
		canceled:  c.canceled,
		deadline:  c.deadline,
		completed: c.completed,
		serverErr: c.serverErr,
	}
}

// issueStressOp performs one of three op variants per (gid,iter) so the
// stress mix covers both wire-level and session-level paths:
//
//   - variant 0: wire-level Walk → Clunk on a unique fid (mirrors
//     concurrent_test.go's base pattern).
//   - variant 1: session-level OpenFile + ReadCtx + Close (exercises
//     Plan 22-03's *Ctx variants end-to-end).
//   - variant 2: wire-level Walk to a nonexistent path — server replies
//     ENOENT; this is a "happy error path" that still completes the
//     round-trip and tests the canceled-op flow around a failing op.
//
// Fids derived from gid + iter + a variant offset so no two iterations
// can collide. Range starts at 1000 to leave low fids for the root
// Attach and the server's reserved pool.
func issueStressOp(ctx context.Context, cli *client.Conn, gid, iter int) error {
	// Compose a unique fid: gid ∈ [0, 500), iter ∈ [0, 10), variant ∈
	// [0, 3). Packed so each (gid, iter) has its own 32-wide block.
	// Block width 32 gives headroom for future variant additions.
	fid := proto.Fid(1000 + uint32(gid)*32 + uint32(iter))
	variant := (gid + iter) % 3

	switch variant {
	case 0:
		// Wire-level Walk + Clunk on a known path.
		_, err := cli.Walk(ctx, proto.Fid(0), fid, []string{"hello.txt"})
		if err != nil {
			return err
		}
		// Clunk with context.Background() so an already-cancelled opCtx
		// doesn't retry the Tflush on cleanup. The fid is still server-
		// bound and we want to release it regardless of the caller's
		// cancellation. Ignore the error — if the Conn is already closed
		// the fid is server-side cleaned up on connection teardown.
		_ = cli.Clunk(context.Background(), fid)
		return nil

	case 1:
		// Session-level OpenFile + ReadCtx + Close. Exercises Plan 22-03's
		// *Ctx path through roundTrip → flushAndWait.
		f, err := cli.OpenFile(ctx, "/hello.txt", 0, 0) // O_RDONLY = 0
		if err != nil {
			return err
		}
		buf := make([]byte, 32)
		_, rerr := f.ReadCtx(ctx, buf)
		// Close with no-op ctx — see Clunk rationale above. File.Close
		// uses the fixed cleanupDeadline per D-24.
		_ = f.Close()
		return rerr

	case 2:
		// Wire-level Walk to a nonexistent name. Server returns ENOENT
		// as a *client.Error; no fid is bound on failure (Phase 19
		// Rwalk semantics: partial walks allocate nothing).
		_, err := cli.Walk(ctx, proto.Fid(0), fid, []string{"nonexistent.txt"})
		return err
	}
	return nil
}

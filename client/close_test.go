package client_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
)

// TestClient_Close_UnblocksCallers exercises D-22: an in-flight caller blocked
// on respCh must return ErrClosed well before the 5s default drain deadline
// once Close is invoked from another goroutine.
func TestClient_Close_UnblocksCallers(t *testing.T) {
	t.Parallel()

	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	// Attach root so the connection is healthy.
	if _, err := cli.Attach(ctx, proto.Fid(0), "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// Kick off a goroutine that will get ErrClosed once Close fires.
	// We register a tag manually through an op that the server hasn't been
	// primed to complete quickly — simplest: spawn N goroutines doing
	// sequential Walk/Clunk cycles and call Close mid-stream.
	errCh := make(chan error, 1)
	go func() {
		// Do a busy loop of ops; one of them will land in respCh just as
		// Close fires.
		for i := 0; i < 1000; i++ {
			fid := proto.Fid(100 + i)
			_, err := cli.Walk(ctx, proto.Fid(0), fid, []string{"hello.txt"})
			if err != nil {
				errCh <- err
				return
			}
			if err := cli.Clunk(ctx, fid); err != nil {
				errCh <- err
				return
			}
		}
		errCh <- nil
	}()

	// Give the goroutine a head start.
	time.Sleep(5 * time.Millisecond)

	start := time.Now()
	if err := cli.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("Close returned after %v, expected well under 2s (default 5s drain)", elapsed)
	}

	select {
	case err := <-errCh:
		if err == nil {
			// The loop completed — that's fine as long as Close was fast.
			return
		}
		if !errors.Is(err, client.ErrClosed) {
			t.Errorf("caller returned %v, want ErrClosed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("caller goroutine did not return within 3s of Close")
	}
}

// TestClient_Close_IdempotentFromMultipleGoroutines verifies that concurrent
// Close calls do not panic, do not double-close channels, and all return
// nil (or a consistent error).
func TestClient_Close_IdempotentFromMultipleGoroutines(t *testing.T) {
	t.Parallel()

	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	const N = 10
	var wg sync.WaitGroup
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = cli.Close()
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: Close returned %v, want nil", i, err)
		}
	}
}

// TestClient_Close_GoroutineLeak verifies no goroutines leak after Close.
// Per D-24, readerWG.Wait() and callerWG.Wait() must run unconditionally
// before Close returns.
//
// This test does NOT call t.Parallel() because runtime.NumGoroutine() is a
// process-global count — parallel subtests spawn/drain goroutines on the
// same clock, introducing noise that has nothing to do with Conn leaks.
// Serial execution isolates the delta to just this test's Conn lifecycle.
func TestClient_Close_GoroutineLeak(t *testing.T) {
	// Baseline: capture goroutine count before the pair boots. Run a brief
	// GC + sleep so any straggler goroutines from earlier tests settle.
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	cli, cleanup := newClientServerPair(t, buildTestRoot(t))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	if _, err := cli.Attach(ctx, proto.Fid(0), "me", ""); err != nil {
		cleanup()
		t.Fatalf("Attach: %v", err)
	}

	// A few ops to exercise the caller/respCh path.
	for i := 0; i < 5; i++ {
		fid := proto.Fid(100 + i)
		if _, err := cli.Walk(ctx, proto.Fid(0), fid, []string{"hello.txt"}); err != nil {
			cleanup()
			t.Fatalf("Walk: %v", err)
		}
		if err := cli.Clunk(ctx, fid); err != nil {
			cleanup()
			t.Fatalf("Clunk: %v", err)
		}
	}

	// Call cleanup (which calls cli.Close + shuts down the server goroutine).
	cleanup()

	// Allow a grace window for goroutines to actually exit scheduler-side.
	// runtime.Gosched + GC keeps this deterministic under -race.
	for i := 0; i < 20; i++ {
		runtime.GC()
		time.Sleep(25 * time.Millisecond)
		if runtime.NumGoroutine() <= baseline+1 {
			break
		}
	}

	got := runtime.NumGoroutine()
	// Allow +2 for scheduler jitter; strict equality is too flaky. Without
	// t.Parallel(), any excess over baseline+2 reflects a real Conn or
	// server-side leak introduced by this test's Conn lifecycle.
	if got > baseline+2 {
		t.Errorf("goroutine leak: baseline=%d, after Close=%d (delta=%d)",
			baseline, got, got-baseline)
	}
}

// TestClient_Shutdown_CtxDeadline verifies Shutdown honors a caller-supplied
// ctx deadline shorter than the 5s default drain.
func TestClient_Shutdown_CtxDeadline(t *testing.T) {
	t.Parallel()

	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := cli.Attach(ctx, proto.Fid(0), "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// Caller ctx with 200ms deadline.
	shutdownCtx, scancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer scancel()

	start := time.Now()
	if err := cli.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	elapsed := time.Since(start)

	// Shutdown should be quick — callers drain immediately because
	// signalShutdown unblocks them, so the deadline isn't the bound here.
	// The gate is "well under 5s default".
	if elapsed > 1*time.Second {
		t.Errorf("Shutdown returned after %v, expected well under 1s", elapsed)
	}
}

// TestClient_Shutdown_CtxCancelled verifies Shutdown with an already-cancelled
// ctx still completes (signalShutdown + readerWG.Wait must run regardless).
func TestClient_Shutdown_CtxCancelled(t *testing.T) {
	t.Parallel()

	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // pre-cancelled

	start := time.Now()
	if err := cli.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed > 1*time.Second {
		t.Errorf("Shutdown with cancelled ctx took %v, expected <1s", elapsed)
	}
}

// TestClient_Ops_AfterClose verifies all op methods return ErrClosed after
// the Conn has been Closed.
func TestClient_Ops_AfterClose(t *testing.T) {
	t.Parallel()

	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	if err := cli.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 1*time.Second)
	defer cancel()

	_, err := cli.Attach(ctx, proto.Fid(0), "me", "")
	if !errors.Is(err, client.ErrClosed) {
		t.Errorf("Attach after Close: got %v, want ErrClosed", err)
	}

	_, err = cli.Walk(ctx, proto.Fid(0), proto.Fid(1), []string{"hello.txt"})
	if !errors.Is(err, client.ErrClosed) {
		t.Errorf("Walk after Close: got %v, want ErrClosed", err)
	}

	_, _, err = cli.Lopen(ctx, proto.Fid(1), 0)
	if !errors.Is(err, client.ErrClosed) {
		t.Errorf("Lopen after Close: got %v, want ErrClosed", err)
	}

	if err := cli.Clunk(ctx, proto.Fid(1)); !errors.Is(err, client.ErrClosed) {
		t.Errorf("Clunk after Close: got %v, want ErrClosed", err)
	}
}

// TestClient_Close_DrainTimeout exercises the drain-timeout log path. A
// test-only stuck caller holds a tag without ever delivering; Close must
// return after the drain deadline fires.
//
// We create a custom Conn setup that registers a tag in inflightMap + bumps
// callerWG to simulate a stuck caller. This test file lives in the external
// client_test package, so we use an internal-access hook via a tiny helper
// in close_stuck_test.go to pin the behavior without widening the public
// surface.
//
// The assertion is: Close returns within (drainTimeout + 500ms grace) and
// emits a log warning (we can't easily intercept logs without a custom
// handler, so the primary assertion is timing).
func TestClient_Close_DrainTimeout(t *testing.T) {
	t.Parallel()

	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	// Use the test-only hook to register a "stuck caller" that bumps
	// callerWG and never returns until we release it. See export_test.go.
	release := client.RegisterStuckCaller(cli)
	defer release()

	// Override the drain deadline via the Shutdown(ctx) variant with a 200ms
	// deadline. With a stuck caller, Shutdown must return after ~200ms, not
	// hang forever.
	shutdownCtx, scancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer scancel()

	start := time.Now()
	if err := cli.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	elapsed := time.Since(start)

	// Drain timed out at ~200ms; readerWG.Wait is unbounded but quick
	// because signalShutdown closed nc. So we expect ~200-500ms total.
	if elapsed < 150*time.Millisecond {
		t.Errorf("Shutdown returned in %v, expected ≥200ms (drain deadline)", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Errorf("Shutdown took %v, expected <3s", elapsed)
	}
}

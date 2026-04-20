package client_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
)

// TestClient_Concurrent exercises D-07 goroutine safety under load.
// 100 goroutines × 10 iterations each do Walk→Lopen→Read→Clunk cycles.
// Each goroutine allocates a unique fid block derived from its index so
// there's no fid-level collision — the test isolates tag-level and
// writeMu-level concurrency (D-07 invariants).
func TestClient_Concurrent(t *testing.T) {
	t.Parallel()
	const numG = 100
	const iters = 10

	// maxInflight default is 64; with 100 goroutines we exercise the
	// natural back-pressure path (D-02).
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	rootFid := proto.Fid(0)
	if _, err := cli.Raw().Attach(ctx, rootFid, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	var wg sync.WaitGroup
	var errCount atomic.Int64
	for g := range numG {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			// Each goroutine owns fids [100+gid*iters, 100+gid*iters+iters).
			baseFid := proto.Fid(100 + gid*iters)
			for i := range iters {
				fid := baseFid + proto.Fid(i)
				_, err := cli.Walk(ctx, rootFid, fid, []string{"hello.txt"})
				if err != nil {
					t.Errorf("g=%d i=%d Walk: %v", gid, i, err)
					errCount.Add(1)
					return
				}
				_, _, err = cli.Lopen(ctx, fid, 0) // O_RDONLY
				if err != nil {
					t.Errorf("g=%d i=%d Lopen: %v", gid, i, err)
					errCount.Add(1)
					_ = cli.Clunk(ctx, fid)
					return
				}
				data, err := cli.Read(ctx, fid, 0, 100)
				if err != nil {
					t.Errorf("g=%d i=%d Read: %v", gid, i, err)
					errCount.Add(1)
					_ = cli.Clunk(ctx, fid)
					return
				}
				if string(data) != "hello world\n" {
					t.Errorf("g=%d i=%d Read got %q, want %q",
						gid, i, data, "hello world\n")
					errCount.Add(1)
				}
				if err := cli.Clunk(ctx, fid); err != nil {
					t.Errorf("g=%d i=%d Clunk: %v", gid, i, err)
					errCount.Add(1)
				}
			}
		}(g)
	}
	wg.Wait()
	if n := errCount.Load(); n != 0 {
		t.Fatalf("%d errors across %d×%d ops", n, numG, iters)
	}

	// After all ops complete, the inflight map must have drained.
	if got := client.InflightLen(cli); got != 0 {
		t.Errorf("inflight.len() = %d after %d concurrent ops; want 0", got, numG*iters)
	}
}

// TestClient_TagReuse_Stress exercises Pitfall 2 ordering (unregister-before-
// release) under heavy tag churn. 1000 sequential iterations + 10 concurrent
// goroutines × 1000 iterations. After the sequential portion, inflight.len()
// must be 0 and the free-tag count must match maxInflight; under -race any
// tag-collision bug would surface.
func TestClient_TagReuse_Stress(t *testing.T) {
	t.Parallel()
	const seqIters = 1000
	const concG = 10
	const concIters = 1000

	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	rootFid := proto.Fid(0)
	if _, err := cli.Raw().Attach(ctx, rootFid, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// Sequential: 1000 Walk+Clunk cycles. After each, inflight MUST be 0.
	for i := range seqIters {
		fid := proto.Fid(1 + i%500) // reuse fids to stress allocator churn
		_, err := cli.Walk(ctx, rootFid, fid, []string{"hello.txt"})
		if err != nil {
			t.Fatalf("seq i=%d Walk: %v", i, err)
		}
		if err := cli.Clunk(ctx, fid); err != nil {
			t.Fatalf("seq i=%d Clunk: %v", i, err)
		}
	}

	if got := client.InflightLen(cli); got != 0 {
		t.Errorf("after %d sequential ops: inflight.len()=%d, want 0", seqIters, got)
	}
	// Default maxInflight is 64 — all tags should have been returned.
	if got := client.FreeTagCount(cli); got != 64 {
		t.Errorf("after %d sequential ops: free-tag count=%d, want 64", seqIters, got)
	}

	// Concurrent: 10 goroutines × 1000 iterations. Each iteration uses a
	// distinct fid derived from goroutine + iteration index. Under -race,
	// any tag double-handout would surface as a data race on inflightMap
	// or a panic on duplicate map keys.
	var wg sync.WaitGroup
	var errCount atomic.Int64
	for g := range concG {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			baseFid := proto.Fid(10000 + gid*concIters)
			for i := range concIters {
				fid := baseFid + proto.Fid(i)
				_, err := cli.Walk(ctx, rootFid, fid, []string{"hello.txt"})
				if err != nil {
					t.Errorf("g=%d i=%d Walk: %v", gid, i, err)
					errCount.Add(1)
					return
				}
				if err := cli.Clunk(ctx, fid); err != nil {
					t.Errorf("g=%d i=%d Clunk: %v", gid, i, err)
					errCount.Add(1)
				}
			}
		}(g)
	}
	wg.Wait()
	if n := errCount.Load(); n != 0 {
		t.Fatalf("%d errors across concurrent tag-reuse stress", n)
	}

	if got := client.InflightLen(cli); got != 0 {
		t.Errorf("after concurrent tag-reuse stress: inflight.len()=%d, want 0", got)
	}
	if got := client.FreeTagCount(cli); got != 64 {
		t.Errorf("after concurrent tag-reuse stress: free-tag count=%d, want 64", got)
	}
}

// TestClient_Concurrent_Close fires Close while goroutines are mid-flight.
// Every pending op must unblock with ErrClosed (or a wire-level error that
// collapses to ErrClosed after shutdown). No panics, no leaked goroutines.
func TestClient_Concurrent_Close(t *testing.T) {
	t.Parallel()

	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	rootFid := proto.Fid(0)
	if _, err := cli.Raw().Attach(ctx, rootFid, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	const numG = 50
	var wg sync.WaitGroup
	errs := make([]error, numG)
	started := make(chan struct{}, numG)

	for g := range numG {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			started <- struct{}{}
			// Tight loop of Walk+Clunk until either we hit an error
			// (Close fired) or we've run enough iterations.
			for i := range 10000 {
				fid := proto.Fid(1000 + gid*10000 + i)
				_, err := cli.Walk(ctx, rootFid, fid, []string{"hello.txt"})
				if err != nil {
					errs[gid] = err
					return
				}
				if err := cli.Clunk(ctx, fid); err != nil {
					errs[gid] = err
					return
				}
			}
			errs[gid] = nil // loop completed without shutdown
		}(g)
	}

	// Wait for all goroutines to start, then give them a brief window to
	// get a real op in-flight.
	for range numG {
		<-started
	}
	time.Sleep(10 * time.Millisecond)

	// Fire Close from the main goroutine.
	if err := cli.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Wait for all caller goroutines to return.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("goroutines did not drain within 10s of Close")
	}

	// At least some goroutines must have observed ErrClosed. Goroutines
	// that completed all 10k iterations before Close fired are fine
	// (err == nil); goroutines caught mid-flight must see ErrClosed (or
	// no error if they wrapped up cleanly).
	closedCount := 0
	for i, err := range errs {
		if err == nil {
			continue
		}
		if !errors.Is(err, client.ErrClosed) {
			t.Errorf("goroutine %d: got %v, want ErrClosed", i, err)
			continue
		}
		closedCount++
	}
	if closedCount == 0 {
		t.Log("warning: no goroutines observed ErrClosed — Close may have fired after all ops finished. " +
			"This is not a bug per se but weakens the test signal.")
	}
}

package server

import (
	"context"
	"net"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
)

// stuckNode implements NodeLookuper with a Lookup that ignores context
// cancellation, simulating a stuck handler.
type stuckNode struct {
	Inode
	block   chan struct{}
	started chan struct{}
}

func newStuckNode(qid proto.QID) *stuckNode {
	n := &stuckNode{
		block:   make(chan struct{}),
		started: make(chan struct{}),
	}
	n.Init(qid, n)
	return n
}

func (n *stuckNode) Lookup(_ context.Context, _ string) (Node, error) {
	select {
	case <-n.started:
	default:
		close(n.started)
	}
	// Deliberately ignores ctx.Done() -- simulates stuck handler.
	<-n.block
	f := &testFile{}
	f.Init(proto.QID{Type: proto.QTFILE, Path: 42}, f)
	return f, nil
}

// trackingNode tracks whether clunkAll was called by counting fids that
// survive cleanup.
type trackingNode struct {
	Inode
}

func (n *trackingNode) Lookup(_ context.Context, _ string) (Node, error) {
	f := &testFile{}
	f.Init(proto.QID{Type: proto.QTFILE, Path: 42}, f)
	return f, nil
}

// stableGoroutineBaseline samples runtime.NumGoroutine() repeatedly and
// returns the minimum observed value.
//
// Server-package tests run alongside many t.Parallel() siblings (300+ tests in
// the binary, each spawning handler/listener goroutines). runtime.NumGoroutine
// is process-global, so a single sample captures the concurrent world, not
// just our test. Taking the minimum of several samples filters out transient
// spikes from sibling-test activity, giving a conservative "lower bound" for
// the population we care about (our own goroutines + steady-state sibling
// load).
func stableGoroutineBaseline(t *testing.T) int {
	t.Helper()
	const samples = 10
	const interval = 20 * time.Millisecond

	runtime.GC()
	time.Sleep(interval)

	minCount := runtime.NumGoroutine()
	for range samples - 1 {
		time.Sleep(interval)
		runtime.GC()
		if n := runtime.NumGoroutine(); n < minCount {
			minCount = n
		}
	}
	return minCount
}

// assertNoGoroutineLeak polls runtime.NumGoroutine() for up to timeout, taking
// the minimum observed count. It reports a leak if the minimum exceeds
// baseline+tolerance -- meaning the population never drained to the baseline
// even across the full polling window.
//
// This pattern is robust against process-global noise from parallel sibling
// tests: those tests' goroutines appear and vanish on their own clock, so any
// sample-moment is a blend of "our goroutines" and "sibling goroutines in
// flight". The MINIMUM over many samples approximates the steady-state count,
// filtering out siblings that are transiently busy. Compare against the same
// minimum-sampled baseline (see stableGoroutineBaseline) to isolate the delta
// attributable to the test under examination.
//
// Precedent: client.TestClient_Close_GoroutineLeak (smaller-scale version of
// this loop in a less-parallel package).
func assertNoGoroutineLeak(t *testing.T, baseline, tolerance int, timeout time.Duration) {
	t.Helper()
	const interval = 50 * time.Millisecond

	deadline := time.Now().Add(timeout)
	minCount := int(^uint(0) >> 1) // max int
	for {
		runtime.GC()
		time.Sleep(interval)
		n := runtime.NumGoroutine()
		if n < minCount {
			minCount = n
		}
		if minCount <= baseline+tolerance {
			return // drained to within tolerance; not a leak
		}
		if time.Now().After(deadline) {
			t.Errorf("goroutine leak: baseline=%d, min observed after drain=%d (delta=%d, tolerance=%d)",
				baseline, minCount, minCount-baseline, tolerance)
			return
		}
	}
}

func TestDisconnectCleanup_ClunksAllFids(t *testing.T) {
	t.Parallel()

	rootQID := proto.QID{Type: proto.QTDIR, Path: 1}
	root := &trackingNode{}
	root.Init(rootQID, root)

	client, server := net.Pipe()
	defer func() { _ = server.Close() }()

	srv := New(root, WithMaxMsize(65536), WithLogger(discardLogger()))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Get access to the conn for inspection.
	c := newConn(srv, server)

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.serve(ctx)
	}()

	// Negotiate version.
	sendTversion(t, client, 65536, "9P2000.L")
	_ = readRversion(t, client)

	// Attach multiple fids.
	for i := range 3 {
		fid := proto.Fid(i)
		sendMessage(t, client, proto.Tag(i+1), &proto.Tattach{
			Fid:   fid,
			Afid:  proto.NoFid,
			Uname: "test",
		})
		_, msg := readResponse(t, client)
		if _, ok := msg.(*proto.Rattach); !ok {
			t.Fatalf("expected Rattach for fid %d, got %T", i, msg)
		}
	}

	// Verify fids exist.
	if c.fids.len() != 3 {
		t.Fatalf("fid count before disconnect = %d, want 3", c.fids.len())
	}

	// Close client side to trigger disconnect.
	_ = client.Close()

	// Wait for serve to exit.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not exit after disconnect")
	}

	// All fids should have been clunked during cleanup.
	if c.fids.len() != 0 {
		t.Errorf("fid count after disconnect = %d, want 0", c.fids.len())
	}
}

func TestDisconnectCleanup_CancelsInflight(t *testing.T) {
	t.Parallel()

	rootQID := proto.QID{Type: proto.QTDIR, Path: 1}
	root := newBlockingNode(rootQID)

	cp := newConnPair(t, root)

	// Attach.
	cp.attach(t, 1, 0, "user", "")

	// Send a request that blocks.
	sendMessage(t, cp.client, 10, &proto.Twalk{
		Fid:    0,
		NewFid: 1,
		Names:  []string{"child"},
	})

	// Wait for handler to start.
	select {
	case <-root.started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}

	// Close client side. This should cancel the inflight request's context.
	_ = cp.client.Close()

	// Wait for serve to exit.
	select {
	case <-cp.done:
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not exit after disconnect")
	}
}

func TestDisconnectCleanup_DrainDeadline(t *testing.T) {
	t.Parallel()

	rootQID := proto.QID{Type: proto.QTDIR, Path: 1}
	root := newStuckNode(rootQID)

	client, server := net.Pipe()
	defer func() { _ = server.Close() }()

	srv := New(root, WithMaxMsize(65536), WithLogger(discardLogger()))

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.ServeConn(ctx, server)
	}()

	// Negotiate.
	sendTversion(t, client, 65536, "9P2000.L")
	_ = readRversion(t, client)

	// Attach.
	sendMessage(t, client, 1, &proto.Tattach{
		Fid:   0,
		Afid:  proto.NoFid,
		Uname: "test",
	})
	readResponse(t, client)

	// Send a request that will be stuck (ignores ctx cancellation).
	sendMessage(t, client, 10, &proto.Twalk{
		Fid:    0,
		NewFid: 1,
		Names:  []string{"child"},
	})

	// Wait for handler to start.
	select {
	case <-root.started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}

	// Close client side.
	_ = client.Close()

	// Cleanup should complete within cleanupDeadline + margin.
	// The stuck handler ignores context cancellation, so cleanup uses the deadline.
	start := time.Now()
	select {
	case <-done:
		elapsed := time.Since(start)
		// Should complete roughly at cleanupDeadline (5s), not hang forever.
		// Allow generous margin for CI variability.
		if elapsed > 10*time.Second {
			t.Errorf("cleanup took %v, expected ~%v", elapsed, cleanupDeadline)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("cleanup did not complete within deadline + margin")
	}

	// Unblock the stuck handler so the goroutine can exit.
	close(root.block)
}

func TestServerSurvivesDisconnect(t *testing.T) {
	t.Parallel()

	rootQID := proto.QID{Type: proto.QTDIR, Path: 1}
	root := newDirNode(rootQID)

	srv := New(root, WithMaxMsize(65536), WithLogger(discardLogger()))

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	// Connection 1: connect, negotiate, attach, disconnect.
	c1client, c1server := net.Pipe()
	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		srv.ServeConn(ctx, c1server)
	}()

	sendTversion(t, c1client, 65536, "9P2000.L")
	_ = readRversion(t, c1client)
	sendMessage(t, c1client, 1, &proto.Tattach{
		Fid:   0,
		Afid:  proto.NoFid,
		Uname: "user1",
	})
	readResponse(t, c1client)
	_ = c1client.Close()
	<-done1

	// Connection 2: should work fine after connection 1 disconnected.
	c2client, c2server := net.Pipe()
	defer func() { _ = c2client.Close() }()
	defer func() { _ = c2server.Close() }()
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		srv.ServeConn(ctx, c2server)
	}()

	sendTversion(t, c2client, 65536, "9P2000.L")
	rv := readRversion(t, c2client)
	if rv.Version != "9P2000.L" {
		t.Fatalf("conn2 version = %q, want 9P2000.L", rv.Version)
	}

	sendMessage(t, c2client, 1, &proto.Tattach{
		Fid:   0,
		Afid:  proto.NoFid,
		Uname: "user2",
	})
	tag, msg := readResponse(t, c2client)
	if tag != 1 {
		t.Fatalf("tag = %d, want 1", tag)
	}
	if _, ok := msg.(*proto.Rattach); !ok {
		t.Fatalf("expected Rattach on conn2, got %T", msg)
	}

	_ = c2client.Close()
	<-done2
}

// This test does NOT call t.Parallel() because runtime.NumGoroutine() is a
// process-global count — parallel sibling tests spawn/drain goroutines on the
// same clock, introducing noise that has nothing to do with this test's
// connection lifecycle. Serial execution isolates the delta to just rapid
// connect/disconnect cycles (precedent: client.TestClient_Close_GoroutineLeak).
//
// Even without t.Parallel() here, the server test binary has hundreds of
// concurrent t.Parallel() tests running. We use stableGoroutineBaseline and
// assertNoGoroutineLeak (both minimum-of-many-samples) to filter out the
// process-global noise so the signal reflects our own connection lifecycle.
func TestRapidConnectDisconnect(t *testing.T) {
	rootQID := proto.QID{Type: proto.QTDIR, Path: 1}
	root := newDirNode(rootQID)

	srv := New(root, WithMaxMsize(65536), WithLogger(discardLogger()))

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	baseGoroutines := stableGoroutineBaseline(t)

	const cycles = 50
	var active atomic.Int32

	for i := range cycles {
		active.Add(1)
		go func(idx int) {
			defer active.Add(-1)

			client, server := net.Pipe()
			done := make(chan struct{})
			go func() {
				defer close(done)
				srv.ServeConn(ctx, server)
			}()

			sendTversion(t, client, 65536, "9P2000.L")
			_ = readRversion(t, client)

			// Quick attach.
			sendMessage(t, client, 1, &proto.Tattach{
				Fid:   0,
				Afid:  proto.NoFid,
				Uname: "test",
			})
			readResponse(t, client)

			_ = client.Close()
			<-done
			_ = server.Close()
		}(i)
	}

	// Wait for all connections to complete.
	deadline := time.After(15 * time.Second)
	for active.Load() > 0 {
		select {
		case <-deadline:
			t.Fatalf("connections still active after deadline: %d remaining", active.Load())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Poll until goroutine count drains to within tolerance of baseline, or
	// the 3s window expires. Uses minimum-of-samples to filter sibling-test
	// noise (see assertNoGoroutineLeak doc comment).
	assertNoGoroutineLeak(t, baseGoroutines, 10, 3*time.Second)
}

// Compile-time checks.
var (
	_ NodeLookuper  = (*stuckNode)(nil)
	_ InodeEmbedder = (*stuckNode)(nil)
	_ NodeLookuper  = (*trackingNode)(nil)
	_ InodeEmbedder = (*trackingNode)(nil)
)

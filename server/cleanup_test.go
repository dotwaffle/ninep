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
	qid     proto.QID
	block   chan struct{}
	started chan struct{}
}

func newStuckNode(qid proto.QID) *stuckNode {
	return &stuckNode{
		qid:     qid,
		block:   make(chan struct{}),
		started: make(chan struct{}),
	}
}

func (n *stuckNode) QID() proto.QID { return n.qid }

func (n *stuckNode) Lookup(_ context.Context, _ string) (Node, error) {
	select {
	case <-n.started:
	default:
		close(n.started)
	}
	// Deliberately ignores ctx.Done() -- simulates stuck handler.
	<-n.block
	return &testFile{qid: proto.QID{Type: proto.QTFILE, Path: 42}}, nil
}

// trackingNode tracks whether clunkAll was called by counting fids that
// survive cleanup.
type trackingNode struct {
	qid proto.QID
}

func (n *trackingNode) QID() proto.QID { return n.qid }

func (n *trackingNode) Lookup(_ context.Context, name string) (Node, error) {
	return &testFile{qid: proto.QID{Type: proto.QTFILE, Path: 42}}, nil
}

func TestDisconnectCleanup_ClunksAllFids(t *testing.T) {
	t.Parallel()

	rootQID := proto.QID{Type: proto.QTDIR, Path: 1}
	root := &trackingNode{qid: rootQID}

	client, server := net.Pipe()
	defer server.Close()

	srv := New(root, WithMaxMsize(65536), WithLogger(discardLogger()))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	client.Close()

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
	cp.client.Close()

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
	defer server.Close()

	srv := New(root, WithMaxMsize(65536), WithLogger(discardLogger()))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	client.Close()

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
	root := &dirNode{
		qid:      rootQID,
		children: map[string]Node{},
	}

	srv := New(root, WithMaxMsize(65536), WithLogger(discardLogger()))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
	c1client.Close()
	<-done1

	// Connection 2: should work fine after connection 1 disconnected.
	c2client, c2server := net.Pipe()
	defer c2client.Close()
	defer c2server.Close()
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

	c2client.Close()
	<-done2
}

func TestRapidConnectDisconnect(t *testing.T) {
	t.Parallel()

	rootQID := proto.QID{Type: proto.QTDIR, Path: 1}
	root := &dirNode{
		qid:      rootQID,
		children: map[string]Node{},
	}

	srv := New(root, WithMaxMsize(65536), WithLogger(discardLogger()))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Warm up the runtime to stabilize goroutine count.
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	baseGoroutines := runtime.NumGoroutine()

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

			client.Close()
			<-done
			server.Close()
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

	// Allow time for goroutine cleanup.
	time.Sleep(100 * time.Millisecond)
	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	finalGoroutines := runtime.NumGoroutine()
	// Allow reasonable tolerance for runtime goroutines.
	tolerance := 10
	if finalGoroutines > baseGoroutines+tolerance {
		t.Errorf("goroutine leak: before=%d, after=%d (tolerance=%d)",
			baseGoroutines, finalGoroutines, tolerance)
	}
}

// Compile-time checks.
var (
	_ NodeLookuper = (*stuckNode)(nil)
	_ NodeLookuper = (*trackingNode)(nil)
)

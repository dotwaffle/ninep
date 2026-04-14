package server

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
)

// TestMaxConnections_RejectsExcess verifies that ServeConn rejects the
// (N+1)th connection immediately when WithMaxConnections(N) is configured.
// The rejected connection must be closed before ServeConn returns.
func TestMaxConnections_RejectsExcess(t *testing.T) {
	t.Parallel()
	root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})
	srv := New(root, WithMaxConnections(1), WithLogger(discardLogger()))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)

	// First connection: accepted; negotiate Tversion to ensure it is serving.
	c1Client, c1Server := net.Pipe()
	t.Cleanup(func() { _ = c1Client.Close() })
	t.Cleanup(func() { _ = c1Server.Close() }) // belt-and-braces
	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		srv.ServeConn(ctx, c1Server)
	}()
	sendTversion(t, c1Client, 65536, "9P2000.L")
	_ = readRversion(t, c1Client)

	// Second connection: must be rejected — ServeConn must return fast.
	c2Client, c2Server := net.Pipe()
	t.Cleanup(func() { _ = c2Client.Close() })
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		srv.ServeConn(ctx, c2Server)
	}()

	select {
	case <-done2:
		// ok — ServeConn returned immediately
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("ServeConn did not return on rejected connection within 500ms")
	}

	// The rejected conn should be closed — read returns error.
	buf := make([]byte, 1)
	_ = c2Client.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, err := c2Client.Read(buf); err == nil {
		t.Fatalf("expected read error on rejected conn, got nil")
	}

	// Clean up c1 — closing client lets the first ServeConn drain.
	_ = c1Client.Close()
	<-done1

	if got := srv.connCount.Load(); got != 0 {
		t.Fatalf("connCount = %d, want 0 after cleanup", got)
	}
}

// TestMaxConnections_ZeroUnlimited verifies that leaving WithMaxConnections
// unset (or passing 0) imposes no limit.
func TestMaxConnections_ZeroUnlimited(t *testing.T) {
	t.Parallel()
	root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})
	srv := New(root, WithLogger(discardLogger()))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)

	for range 3 {
		cc, sc := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			srv.ServeConn(ctx, sc)
		}()
		sendTversion(t, cc, 65536, "9P2000.L")
		_ = readRversion(t, cc)
		_ = cc.Close()
		<-done
	}

	if got := srv.connCount.Load(); got != 0 {
		t.Fatalf("connCount = %d, want 0 (unlimited mode should not touch counter)", got)
	}
}

// TestMaxConnections_NoCounterLeak verifies that after many sequential
// connections, connCount returns to 0 (defer Add(-1) runs on every exit path).
func TestMaxConnections_NoCounterLeak(t *testing.T) {
	t.Parallel()
	root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})
	srv := New(root, WithMaxConnections(2), WithLogger(discardLogger()))

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)

	for range 20 {
		cc, sc := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			srv.ServeConn(ctx, sc)
		}()
		sendTversion(t, cc, 65536, "9P2000.L")
		_ = readRversion(t, cc)
		_ = cc.Close()
		<-done
	}

	if got := srv.connCount.Load(); got != 0 {
		t.Fatalf("connCount = %d, want 0 after all connections closed", got)
	}
}

// TestMaxConnections_ConcurrentAccept launches 2N goroutines each calling
// ServeConn concurrently. Exactly N should successfully negotiate Tversion;
// the other N should be rejected. After all exit, connCount must be 0.
func TestMaxConnections_ConcurrentAccept(t *testing.T) {
	t.Parallel()
	const N = 8
	root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})
	srv := New(root, WithMaxConnections(N), WithLogger(discardLogger()))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)

	var wg sync.WaitGroup
	var accepted, rejected atomic.Int64
	clients := make([]net.Conn, 2*N)
	servers := make([]net.Conn, 2*N)
	for i := range 2 * N {
		cc, sc := net.Pipe()
		clients[i] = cc
		servers[i] = sc
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv.ServeConn(ctx, sc)
		}()
	}

	var negWg sync.WaitGroup
	for _, cc := range clients {
		negWg.Add(1)
		go func(c net.Conn) {
			defer negWg.Done()
			_ = c.SetDeadline(time.Now().Add(2 * time.Second))
			if err := writeTversionRaw(c, 65536, "9P2000.L"); err != nil {
				rejected.Add(1)
				return
			}
			if _, err := readRversionOrErr(c); err != nil {
				rejected.Add(1)
				return
			}
			accepted.Add(1)
		}(cc)
	}
	negWg.Wait()

	// Close all clients to let servers drain.
	for _, cc := range clients {
		_ = cc.Close()
	}
	wg.Wait()

	if got := accepted.Load(); got != N {
		t.Fatalf("accepted = %d, want %d", got, N)
	}
	if got := rejected.Load(); got != N {
		t.Fatalf("rejected = %d, want %d", got, N)
	}
	if got := srv.connCount.Load(); got != 0 {
		t.Fatalf("connCount = %d, want 0", got)
	}
}

// writeTversionRaw is an err-returning variant of sendTversion for tests that
// need to observe write failures (e.g. when the server rejected and closed
// the conn before we wrote).
func writeTversionRaw(w net.Conn, msize uint32, version string) error {
	var body bytes.Buffer
	tv := &proto.Tversion{Msize: msize, Version: version}
	if err := tv.EncodeTo(&body); err != nil {
		return err
	}
	size := uint32(proto.HeaderSize) + uint32(body.Len())
	if err := proto.WriteUint32(w, size); err != nil {
		return err
	}
	if err := proto.WriteUint8(w, uint8(proto.TypeTversion)); err != nil {
		return err
	}
	if err := proto.WriteUint16(w, uint16(proto.NoTag)); err != nil {
		return err
	}
	_, err := w.Write(body.Bytes())
	return err
}

// readRversionOrErr is an err-returning variant of readRversion.
func readRversionOrErr(r net.Conn) (*proto.Rversion, error) {
	size, err := proto.ReadUint32(r)
	if err != nil {
		return nil, err
	}
	if _, err := proto.ReadUint8(r); err != nil { // type
		return nil, err
	}
	if _, err := proto.ReadUint16(r); err != nil { // tag
		return nil, err
	}
	bodySize := int64(size) - int64(proto.HeaderSize)
	var rv proto.Rversion
	if err := rv.DecodeFrom(io.LimitReader(r, bodySize)); err != nil {
		return nil, err
	}
	return &rv, nil
}

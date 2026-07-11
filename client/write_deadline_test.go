package client

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
)

// TestWriteT_WedgedPeerTripsDeadlineAndShutsDown: a peer that never
// drains its end blocks writeT (net.Pipe writes are synchronous). The
// coarse write deadline derived from flushGrace must trip, the write
// must fail with a timeout, and the Conn must be torn down -- a
// partial frame may be on the wire, so the stream cannot carry
// another request.
func TestWriteT_WedgedPeerTripsDeadlineAndShutsDown(t *testing.T) {
	t.Parallel()

	c, _ := newTestConn(t) // server side never reads: the wedged peer
	c.flushGrace = 50 * time.Millisecond

	start := time.Now()
	_, err := c.writeT(1, &proto.Tclunk{Fid: 1})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("writeT succeeded against a peer that never reads")
	}
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("writeT err = %v, want a net.Error timeout", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("writeT blocked %v; the 50ms write deadline did not trip", elapsed)
	}
	if !c.isClosed() {
		t.Error("Conn still open after a mid-frame write failure; stream framing is lost and must not be reused")
	}
}

// TestWriteT_ZeroFlushGraceSkipsDeadline: hand-built Conns with a
// zero-value flushGrace must not arm an already-expired deadline. The
// write should proceed normally once the peer drains it.
func TestWriteT_ZeroFlushGraceSkipsDeadline(t *testing.T) {
	t.Parallel()

	c, srvNC := newTestConn(t)
	c.flushGrace = 0

	done := make(chan error, 1)
	go func() {
		_, err := c.writeT(1, &proto.Tclunk{Fid: 1})
		done <- err
	}()

	if tag := srvDrainOne(t, srvNC); tag != 1 {
		t.Fatalf("drained tag = %v, want 1", tag)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("writeT with zero flushGrace: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writeT did not complete")
	}
}

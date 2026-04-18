package client

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
	"github.com/dotwaffle/ninep/proto/p9u"
)

// newTestConn constructs a minimal *Conn for unit-testing the roundTrip
// helper without running Dial. The nc field is backed by a net.Pipe; callers
// that do not exercise the wire path can ignore it. tagAllocator,
// inflightMap, codec, and closeCh are wired so roundTrip's select branches
// all operate correctly.
//
// Caller invariants: t.Cleanup closes both ends of the pipe and drains any
// spawned read loop. The returned conn has dialect == protocolL by default.
func newTestConn(t *testing.T) (*Conn, net.Conn) {
	t.Helper()
	cliNC, srvNC := net.Pipe()
	t.Cleanup(func() {
		_ = cliNC.Close()
		_ = srvNC.Close()
	})
	c := &Conn{
		nc:       cliNC,
		dialect:  protocolL,
		msize:    65536,
		codec:    codecL,
		tags:     newTagAllocator(8),
		inflight: newInflightMap(),
		closeCh:  make(chan struct{}),
		logger:   slog.New(slog.NewTextHandler(discardWriter{}, nil)),
	}
	return c, srvNC
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// srvDrainOne reads exactly one framed T-message from srvNC, returning the
// tag. Used by tests that need to drain writeT output before delivering a
// response manually via c.inflight.deliver.
func srvDrainOne(t *testing.T, srvNC net.Conn) proto.Tag {
	t.Helper()
	hdr := make([]byte, proto.HeaderSize)
	if _, err := readFull(srvNC, hdr); err != nil {
		t.Fatalf("read header: %v", err)
	}
	size := uint32(hdr[0]) | uint32(hdr[1])<<8 | uint32(hdr[2])<<16 | uint32(hdr[3])<<24
	tag := proto.Tag(uint16(hdr[5]) | uint16(hdr[6])<<8)
	body := make([]byte, int(size)-int(proto.HeaderSize))
	if _, err := readFull(srvNC, body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	return tag
}

func readFull(r net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// TestRoundTrip_HappyPath: roundTrip returns the delivered message for the
// happy-path Rclunk response.
func TestRoundTrip_HappyPath(t *testing.T) {
	t.Parallel()
	c, srvNC := newTestConn(t)

	resultCh := make(chan struct {
		msg proto.Message
		err error
	}, 1)
	go func() {
		msg, err := c.roundTrip(context.Background(), &proto.Tclunk{Fid: 1})
		resultCh <- struct {
			msg proto.Message
			err error
		}{msg, err}
	}()

	tag := srvDrainOne(t, srvNC)
	c.inflight.deliver(tag, &proto.Rclunk{})

	select {
	case r := <-resultCh:
		if r.err != nil {
			t.Fatalf("roundTrip err: %v", r.err)
		}
		if _, ok := r.msg.(*proto.Rclunk); !ok {
			t.Fatalf("expected *Rclunk, got %T", r.msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("roundTrip did not return in time")
	}
}

// TestRoundTrip_RlerrorTranslatedByCaller: roundTrip returns Rlerror msg as-is;
// the caller translates via toError.
func TestRoundTrip_RlerrorTranslatedByCaller(t *testing.T) {
	t.Parallel()
	c, srvNC := newTestConn(t)

	resultCh := make(chan proto.Message, 1)
	go func() {
		msg, err := c.roundTrip(context.Background(), &proto.Tclunk{Fid: 1})
		if err != nil {
			t.Errorf("roundTrip err: %v", err)
		}
		resultCh <- msg
	}()
	tag := srvDrainOne(t, srvNC)
	c.inflight.deliver(tag, &p9l.Rlerror{Ecode: proto.EACCES})

	select {
	case msg := <-resultCh:
		err := toError(msg)
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("toError returned %T (%v), want *Error", err, err)
		}
		if e.Errno != proto.EACCES {
			t.Errorf("Errno = %v, want EACCES", e.Errno)
		}
		if !errors.Is(err, proto.EACCES) {
			t.Errorf("errors.Is(err, EACCES) = false, want true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("roundTrip did not return in time")
	}
}

// TestRoundTrip_RerrorTranslatedByCaller: Rerror (.u) carries both Errno and
// Ename; toError propagates both.
func TestRoundTrip_RerrorTranslatedByCaller(t *testing.T) {
	t.Parallel()
	c, srvNC := newTestConn(t)
	c.dialect = protocolU
	c.codec = codecU

	resultCh := make(chan proto.Message, 1)
	go func() {
		msg, err := c.roundTrip(context.Background(), &proto.Tclunk{Fid: 1})
		if err != nil {
			t.Errorf("roundTrip err: %v", err)
		}
		resultCh <- msg
	}()
	tag := srvDrainOne(t, srvNC)
	c.inflight.deliver(tag, &p9u.Rerror{Ename: "no such file", Errno: proto.ENOENT})

	select {
	case msg := <-resultCh:
		err := toError(msg)
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("toError returned %T, want *Error", err)
		}
		if e.Errno != proto.ENOENT {
			t.Errorf("Errno = %v, want ENOENT", e.Errno)
		}
		if e.Msg != "no such file" {
			t.Errorf("Msg = %q, want 'no such file'", e.Msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("roundTrip did not return in time")
	}
}

// TestRoundTrip_CtxCancelDuringWait: ctx cancel while caller is blocked on
// respCh causes roundTrip to route through flushAndWait, send Tflush, and
// return a ctx.Err()-wrapped error once the first frame (Rflush here) lands.
// Phase 22 (CLIENT-04) change: the test must now drain TWO T-messages from
// the wire (the original Tclunk + the Tflush) and deliver an Rflush so
// flushAndWait unblocks; the Phase 19 single-drain path would hang waiting
// for the flush response.
func TestRoundTrip_CtxCancelDuringWait(t *testing.T) {
	t.Parallel()
	c, srvNC := newTestConn(t)

	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan error, 1)
	go func() {
		_, err := c.roundTrip(ctx, &proto.Tclunk{Fid: 1})
		resultCh <- err
	}()

	origTag := srvDrainOne(t, srvNC) // drain the original Tclunk
	_ = origTag
	// Give the goroutine a moment to block on respCh before cancelling.
	time.Sleep(20 * time.Millisecond)
	cancel()

	// Drain the Tflush that flushAndWait emits and deliver an Rflush
	// back via the inflight map (mirrors how the real readLoop would
	// route the server's Rflush response).
	flushTag := srvDrainOne(t, srvNC)
	c.inflight.deliver(flushTag, &proto.Rflush{})

	select {
	case err := <-resultCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("roundTrip err = %v, want context.Canceled in chain", err)
		}
		// Rflush arrived first (the only frame we delivered), so
		// ErrFlushed must also be in the chain per D-05.
		if !errors.Is(err, ErrFlushed) {
			t.Errorf("roundTrip err = %v, want ErrFlushed in chain (Rflush-first)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("roundTrip did not return after cancel")
	}

	// No leak: both the original tag AND the flushTag are released,
	// inflight map empty. newTagAllocator(8) seeds 8 tags; both were
	// acquired and released, so free-list is back to 8.
	if n := c.inflight.len(); n != 0 {
		t.Errorf("inflight.len = %d, want 0", n)
	}
	if len(c.tags.free) != 8 {
		t.Errorf("tags.free = %d, want 8 (all tags released)", len(c.tags.free))
	}
}

// TestRoundTrip_ConnClosedDuringWait: signalShutdown while the caller is
// blocked on respCh; roundTrip returns ErrClosed.
func TestRoundTrip_ConnClosedDuringWait(t *testing.T) {
	t.Parallel()
	c, srvNC := newTestConn(t)

	resultCh := make(chan error, 1)
	go func() {
		_, err := c.roundTrip(context.Background(), &proto.Tclunk{Fid: 1})
		resultCh <- err
	}()

	_ = srvDrainOne(t, srvNC)
	time.Sleep(20 * time.Millisecond)

	// Simulate signalShutdown without the full Conn lifecycle (closeCh + cancelAll).
	close(c.closeCh)
	c.inflight.cancelAll()

	select {
	case err := <-resultCh:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("roundTrip err = %v, want ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("roundTrip did not return after close")
	}
}

// TestRoundTrip_PreClosedConn: if the Conn is already closed when roundTrip
// is called, it returns ErrClosed without touching the tag allocator.
func TestRoundTrip_PreClosedConn(t *testing.T) {
	t.Parallel()
	c, _ := newTestConn(t)
	close(c.closeCh)

	_, err := c.roundTrip(context.Background(), &proto.Tclunk{Fid: 1})
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("roundTrip err = %v, want ErrClosed", err)
	}
	// No tag was acquired.
	if len(c.tags.free) != 8 {
		t.Errorf("tags.free = %d, want 8", len(c.tags.free))
	}
}

// TestRoundTrip_TagLeakOnWriteError: writeT fails (nc closed); roundTrip
// returns the error and cleans up both the tag and the inflight entry.
func TestRoundTrip_TagLeakOnWriteError(t *testing.T) {
	t.Parallel()
	c, srvNC := newTestConn(t)

	// Close the server side of the pipe so writeT's Write returns an error.
	_ = srvNC.Close()
	// Give the closure a moment to propagate.
	time.Sleep(10 * time.Millisecond)

	_, err := c.roundTrip(context.Background(), &proto.Tclunk{Fid: 1})
	if err == nil {
		t.Fatal("roundTrip returned nil err, want write error")
	}
	if n := c.inflight.len(); n != 0 {
		t.Errorf("inflight.len = %d, want 0 (leaked entry)", n)
	}
	if len(c.tags.free) != 8 {
		t.Errorf("tags.free = %d, want 8 (leaked tag)", len(c.tags.free))
	}
}

// TestToError_NonError returns nil for messages that are not server errors.
func TestToError_NonError(t *testing.T) {
	t.Parallel()
	if err := toError(&proto.Rclunk{}); err != nil {
		t.Errorf("toError(Rclunk) = %v, want nil", err)
	}
	if err := toError(&proto.Rattach{}); err != nil {
		t.Errorf("toError(Rattach) = %v, want nil", err)
	}
	if err := toError(nil); err != nil {
		t.Errorf("toError(nil) = %v, want nil", err)
	}
}

// TestExpectRType_Match: no error when msg type matches one of the allowed.
func TestExpectRType_Match(t *testing.T) {
	t.Parallel()
	if err := expectRType(&proto.Rclunk{}, proto.TypeRclunk); err != nil {
		t.Errorf("expectRType: %v, want nil", err)
	}
	if err := expectRType(&proto.Rwalk{}, proto.TypeRclunk, proto.TypeRwalk); err != nil {
		t.Errorf("expectRType: %v, want nil", err)
	}
}

// TestExpectRType_Mismatch: returns a descriptive error (no panic).
func TestExpectRType_Mismatch(t *testing.T) {
	t.Parallel()
	err := expectRType(&proto.Rversion{}, proto.TypeRclunk)
	if err == nil {
		t.Fatal("expectRType: nil, want mismatch error")
	}
}

// TestExpectRType_Nil returns an error for nil msg.
func TestExpectRType_Nil(t *testing.T) {
	t.Parallel()
	err := expectRType(nil, proto.TypeRclunk)
	if err == nil {
		t.Fatal("expectRType(nil): nil, want error")
	}
}

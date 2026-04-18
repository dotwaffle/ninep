package client_test

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// flushMockServer is a hand-rolled 9P2000.L mock server built on top of a
// raw net.Conn. It gives the test explicit control over the ordering of
// Rread vs Rflush responses so the first-frame-wins logic in
// client.flushAndWait (D-04, D-06) can be exercised deterministically.
//
// The server's behaviour on receiving a T-message is configured via three
// atomics on flushMockServer:
//
//   - rreadGate: chan struct{}. The server's Tread handler blocks on a
//     receive from this chan before replying with Rread. Close it to
//     release the Rread; leave it open to keep the Rread parked on the
//     server side until Tflush arrives (or forever).
//
//   - rflushDelay: time.Duration (stored as int64 nanoseconds via atomic).
//     When the server receives a Tflush it sleeps for this duration
//     BEFORE sending Rflush. Used to force Rread-first ordering: set
//     rflushDelay > 0 and then release rreadGate. The Rread races the
//     sleeping Rflush goroutine and wins.
//
//   - rflushSendImmediately: atomic.Bool. When false (the default), the
//     server waits on rflushGate before sending Rflush. This lets the
//     test assert Rflush-first ordering: close rflushGate while the
//     Rread is still parked on rreadGate, so Rflush arrives at the
//     client first.
//
// The server also counts Tflush frames observed on the wire (tflushCount)
// so the double-flush-guard test (Pitfall 1) can assert exactly one
// Tflush per caller ctx.Cancel.
//
// Protocol scope: enough of .L to run Tversion → Tattach → Twalk →
// Tlopen → Tread. No writes, no directory ops, no xattr — Phase 22 only
// exercises the roundTrip ctx.Done path, which is dialect-neutral.
type flushMockServer struct {
	nc net.Conn

	// rreadGate blocks the server's Tread response until closed.
	rreadGate chan struct{}

	// rflushGate blocks the server's Tflush response until closed; used
	// by the R-first test to hold the Rflush so the original Rread wins
	// the race. rflushSendImmediately overrides this to skip the gate.
	rflushGate chan struct{}

	// rflushSendImmediately, when true, skips rflushGate entirely and
	// sends Rflush as soon as the Tflush frame is decoded. Use for the
	// Rflush-first test (leave rreadGate open so Rread never comes).
	rflushSendImmediately atomic.Bool

	// tflushCount is the number of Tflush frames observed on the wire.
	// Used by the Pitfall 1 double-flush-guard test.
	tflushCount atomic.Int64

	// writeMu serialises R-message writes so a goroutine-driven Rflush
	// doesn't interleave mid-frame with an Rread goroutine.
	writeMu sync.Mutex
}

// newFlushMockServer starts the mock server goroutine on srvNC and
// returns a handle for tests to drive it. t.Cleanup closes srvNC and
// releases both gates so a leaked test doesn't park the server
// goroutine indefinitely.
func newFlushMockServer(tb testing.TB, srvNC net.Conn) *flushMockServer {
	tb.Helper()
	s := &flushMockServer{
		nc:         srvNC,
		rreadGate:  make(chan struct{}),
		rflushGate: make(chan struct{}),
	}
	tb.Cleanup(func() {
		// Defensive: release both gates so the handler goroutines exit
		// cleanly even if the test path didn't reach the release point.
		select {
		case <-s.rreadGate:
		default:
			close(s.rreadGate)
		}
		select {
		case <-s.rflushGate:
		default:
			close(s.rflushGate)
		}
		_ = srvNC.Close()
	})
	go s.serve()
	return s
}

// releaseRread unblocks the Tread handler so it sends Rread.
func (s *flushMockServer) releaseRread() {
	close(s.rreadGate)
}

// releaseRflush unblocks the Tflush handler so it sends Rflush. Only
// meaningful when rflushSendImmediately is false.
func (s *flushMockServer) releaseRflush() {
	close(s.rflushGate)
}

// serve is the top-level read loop. Runs in a goroutine spawned by
// newFlushMockServer.
func (s *flushMockServer) serve() {
	// 1. Read Tversion and reply Rversion(.L).
	tag, msg, err := readFrame(s.nc)
	if err != nil {
		return
	}
	if _, ok := msg.(*proto.Tversion); !ok {
		return
	}
	s.writeMu.Lock()
	_ = p9l.Encode(s.nc, tag, &proto.Rversion{Msize: 65536, Version: "9P2000.L"})
	s.writeMu.Unlock()

	// 2. Subsequent frames: dispatch by concrete type.
	for {
		tag, msg, err := readFrame(s.nc)
		if err != nil {
			return
		}
		switch m := msg.(type) {
		case *proto.Tattach:
			_ = m
			s.respond(tag, &proto.Rattach{QID: proto.QID{Type: proto.QTDIR, Path: 1}})
		case *proto.Twalk:
			// Synthetic Rwalk: one QID per name (file).
			qids := make([]proto.QID, len(m.Names))
			for i := range qids {
				qids[i] = proto.QID{Type: proto.QTFILE, Path: 42 + uint64(i)}
			}
			s.respond(tag, &proto.Rwalk{QIDs: qids})
		case *p9l.Tlopen:
			s.respond(tag, &p9l.Rlopen{QID: proto.QID{Type: proto.QTFILE, Path: 42}, IOUnit: 4096})
		case *proto.Tclunk:
			s.respond(tag, &proto.Rclunk{})
		case *proto.Tread:
			// Parked Tread: launch a goroutine that blocks on the gate,
			// then sends Rread. Done in a goroutine so the server main
			// loop can process the subsequent Tflush concurrently.
			go s.handleRead(tag)
		case *proto.Tflush:
			s.tflushCount.Add(1)
			go s.handleFlush(tag)
		default:
			// Anything else: send a minimal Rlerror so the client isn't
			// wedged waiting for an unimplemented response.
			s.respond(tag, &p9l.Rlerror{Ecode: proto.ENOSYS})
		}
	}
}

// handleRead parks until rreadGate closes, then sends Rread with 4
// bytes of payload ("slow"). If the gate closes via the test's Cleanup
// before a real release, we still send the Rread so the client
// exercises the late-arrival drop path.
func (s *flushMockServer) handleRead(tag proto.Tag) {
	<-s.rreadGate
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = p9l.Encode(s.nc, tag, &proto.Rread{Data: []byte("slow")})
}

// handleFlush sends Rflush, either immediately (rflushSendImmediately)
// or after rflushGate closes. Always sends exactly one Rflush per
// Tflush observed.
func (s *flushMockServer) handleFlush(tag proto.Tag) {
	if !s.rflushSendImmediately.Load() {
		<-s.rflushGate
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = p9l.Encode(s.nc, tag, &proto.Rflush{})
}

// respond is the synchronous "read T, write R" shortcut for ops that
// don't need timing control.
func (s *flushMockServer) respond(tag proto.Tag, resp proto.Message) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = p9l.Encode(s.nc, tag, resp)
}

// readFrame reads one 9P2000.L frame (size[4] + body) off nc and
// decodes it via p9l.Decode. Returns the tag, decoded message, and any
// error. Used by the mock server's read loop.
func readFrame(nc net.Conn) (proto.Tag, proto.Message, error) {
	var sizeBuf [4]byte
	if _, err := io.ReadFull(nc, sizeBuf[:]); err != nil {
		return 0, nil, err
	}
	size := binary.LittleEndian.Uint32(sizeBuf[:])
	// Concatenate size prefix + body and hand to p9l.Decode which
	// expects the full frame (size[4] + body).
	body := make([]byte, size)
	copy(body[:4], sizeBuf[:])
	if _, err := io.ReadFull(nc, body[4:]); err != nil {
		return 0, nil, err
	}
	tag, msg, err := p9l.Decode(newMemReader(body))
	return tag, msg, err
}

// newMemReader wraps b in a simple io.Reader. Avoids pulling in bytes
// just for a Reader; tests don't need bytes.Reader's Seeker/ReaderAt.
type memReader struct {
	b []byte
	i int
}

func newMemReader(b []byte) *memReader { return &memReader{b: b} }

func (r *memReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}

// newFlushTestPair wires a client against a flushMockServer over a
// net.Pipe and returns both the client and the server handle. Cleanup
// closes the client; the mock server's cleanup is registered by
// newFlushMockServer.
func newFlushTestPair(tb testing.TB) (*client.Conn, *flushMockServer, func()) {
	tb.Helper()
	cliNC, srvNC := net.Pipe()
	srv := newFlushMockServer(tb, srvNC)

	dialCtx, dialCancel := context.WithTimeout(tb.Context(), 3*time.Second)
	defer dialCancel()

	cli, err := client.Dial(dialCtx, cliNC,
		client.WithMsize(65536),
		client.WithLogger(discardLogger()),
	)
	if err != nil {
		_ = cliNC.Close()
		tb.Fatalf("Dial: %v", err)
	}
	cleanup := func() {
		_ = cli.Close()
	}
	return cli, srv, cleanup
}

// attachAndOpen attaches the root and opens hello.txt, returning the
// fid the test can use for a Tread. Panics on error because a failing
// prelude means the test harness is broken — the subtest assertions
// aren't the thing under test here.
func attachAndOpen(tb testing.TB, cli *client.Conn) proto.Fid {
	tb.Helper()
	ctx, cancel := context.WithTimeout(tb.Context(), 3*time.Second)
	defer cancel()

	if _, err := cli.Raw().Attach(ctx, proto.Fid(0), "me", ""); err != nil {
		tb.Fatalf("Attach: %v", err)
	}
	fid := proto.Fid(1)
	if _, err := cli.Walk(ctx, proto.Fid(0), fid, []string{"slow.txt"}); err != nil {
		tb.Fatalf("Walk: %v", err)
	}
	if _, _, err := cli.Lopen(ctx, fid, 0); err != nil {
		tb.Fatalf("Lopen: %v", err)
	}
	return fid
}

// TestFlushAndWait_Ordering_RFirst verifies D-05's R-first path: when
// the original Rread arrives before Rflush, the caller's error chain
// satisfies errors.Is(err, ctx.Err()) but NOT errors.Is(err, ErrFlushed).
func TestFlushAndWait_Ordering_RFirst(t *testing.T) {
	t.Parallel()
	cli, srv, cleanup := newFlushTestPair(t)
	defer cleanup()

	fid := attachAndOpen(t, cli)

	// Set up: release Rread quickly, hold Rflush. Rread will win.
	// Order matters — arm the Rflush-hold BEFORE cancelling so the race
	// is deterministic.
	readCtx, readCancel := context.WithCancel(t.Context())

	type result struct {
		data []byte
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		data, err := cli.Read(readCtx, fid, 0, 4096)
		resCh <- result{data: data, err: err}
	}()

	// Give the Tread a moment to hit the server's gate (net.Pipe is
	// synchronous, but the client-side goroutine scheduling still needs
	// a beat to get the frame out and the server to dispatch it).
	time.Sleep(20 * time.Millisecond)

	// Cancel the read. flushAndWait will now send Tflush.
	readCancel()

	// Give the Tflush time to hit the server AND let the server's
	// Tflush handler park on rflushGate. Then release the Rread. The
	// Rread should arrive at the client BEFORE we release rflushGate,
	// so the origCh arm of flushAndWait's inner select wins.
	time.Sleep(20 * time.Millisecond)
	srv.releaseRread()

	// The client's Read call should return now. Don't release the
	// flush gate — the late-arriving Rflush should be dropped by
	// inflight.deliver's unregistered-tag path (Pitfall 7).
	var res result
	select {
	case res = <-resCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Read did not return within 2s of Rread release")
	}

	// Now release the Rflush gate so the server's handleFlush
	// goroutine exits cleanly during Cleanup.
	srv.releaseRflush()

	if res.err == nil {
		t.Fatalf("Read returned nil err; want context.Canceled-wrapped error")
	}
	if !errors.Is(res.err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false; want true. err = %v", res.err)
	}
	if errors.Is(res.err, client.ErrFlushed) {
		t.Errorf("errors.Is(err, ErrFlushed) = true on R-first path; want false. err = %v", res.err)
	}

	// Drain window: let the late Rflush be delivered+dropped.
	// inflight.deliver will find the flushTag unregistered and send to
	// putCachedRMsg. Give the read loop a scheduling beat.
	time.Sleep(50 * time.Millisecond)

	if got := client.InflightLen(cli); got != 0 {
		t.Errorf("InflightLen after flush = %d; want 0", got)
	}
	// After the flush cycle, both the original tag and the flushTag
	// are released. We had tag=1 for the initial Attach/Walk/Lopen
	// pipeline (those are clunked on no fid — they use tag, then
	// release). FreeTagCount should be back to the default 64.
	if got := client.FreeTagCount(cli); got != 64 {
		t.Errorf("FreeTagCount after flush = %d; want 64", got)
	}
}

// TestFlushAndWait_Ordering_RflushFirst verifies D-05's Rflush-first
// path: when Rflush arrives before the original Rread, the caller's
// error chain satisfies BOTH errors.Is(err, ctx.Err()) AND
// errors.Is(err, ErrFlushed).
func TestFlushAndWait_Ordering_RflushFirst(t *testing.T) {
	t.Parallel()
	cli, srv, cleanup := newFlushTestPair(t)
	defer cleanup()

	fid := attachAndOpen(t, cli)

	// Configure: Rflush sends immediately (no gate wait). Rread stays
	// parked on rreadGate until the test releases it (or Cleanup does).
	srv.rflushSendImmediately.Store(true)

	readCtx, readCancel := context.WithCancel(t.Context())

	type result struct {
		data []byte
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		data, err := cli.Read(readCtx, fid, 0, 4096)
		resCh <- result{data: data, err: err}
	}()

	// Wait for Tread to hit the wire, then cancel. The server's Tflush
	// handler will fire Rflush immediately while Rread is still parked.
	time.Sleep(20 * time.Millisecond)
	readCancel()

	var res result
	select {
	case res = <-resCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Read did not return within 2s after ctx cancel")
	}

	// Release the Rread so the server's handleRead goroutine exits
	// cleanly during Cleanup; the client's read-loop drops it via the
	// unregistered-tag path (Pitfall 7).
	srv.releaseRread()

	if res.err == nil {
		t.Fatalf("Read returned nil err; want flush-and-ctx chain")
	}
	if !errors.Is(res.err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false; want true. err = %v", res.err)
	}
	if !errors.Is(res.err, client.ErrFlushed) {
		t.Errorf("errors.Is(err, ErrFlushed) = false on Rflush-first path; want true. err = %v", res.err)
	}

	time.Sleep(50 * time.Millisecond)

	if got := client.InflightLen(cli); got != 0 {
		t.Errorf("InflightLen after flush = %d; want 0", got)
	}
	if got := client.FreeTagCount(cli); got != 64 {
		t.Errorf("FreeTagCount after flush = %d; want 64", got)
	}
}

// TestFlushAndWait_CloseDuringFlush exercises the closeCh arm of
// flushAndWait's inner select (D-19, D-21, Pitfall 5): Conn.Close
// fires while flushAndWait is parked waiting for a response, and the
// caller sees ErrClosed.
func TestFlushAndWait_CloseDuringFlush(t *testing.T) {
	t.Parallel()
	cli, _, _ := newFlushTestPair(t)
	// Do NOT register the default cleanup — this test drives Close
	// explicitly and a second Close would be a no-op but obscures
	// intent.

	fid := attachAndOpen(t, cli)

	readCtx, readCancel := context.WithCancel(t.Context())

	type result struct {
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		_, err := cli.Read(readCtx, fid, 0, 4096)
		resCh <- result{err: err}
	}()

	// Let Tread reach the server, then cancel. flushAndWait will send
	// Tflush and park on its inner select. Neither gate is released,
	// so only closeCh can unblock it.
	time.Sleep(20 * time.Millisecond)
	readCancel()

	// Give flushAndWait a beat to send Tflush and park. Then Close.
	time.Sleep(20 * time.Millisecond)
	go func() {
		_ = cli.Close()
	}()

	var res result
	select {
	case res = <-resCh:
	case <-time.After(3 * time.Second):
		t.Fatal("Read did not return within 3s of Close")
	}

	if res.err == nil {
		t.Fatalf("Read returned nil err; want ErrClosed")
	}
	if !errors.Is(res.err, client.ErrClosed) {
		// Per Pitfall 5, closeCh-first is allowed to lose the ctx
		// cause; the caller MUST see ErrClosed.
		t.Errorf("errors.Is(err, ErrClosed) = false; want true. err = %v", res.err)
	}
}

// TestFlushAndWait_TagReuse verifies that after a flushAndWait
// completes, BOTH the original tag and the flushTag are released back
// to the free-list and reusable by subsequent ops.
func TestFlushAndWait_TagReuse(t *testing.T) {
	t.Parallel()
	cli, srv, cleanup := newFlushTestPair(t)
	defer cleanup()

	fid := attachAndOpen(t, cli)

	// Run Rflush-first path — simpler, no gate timing juggling.
	srv.rflushSendImmediately.Store(true)

	readCtx, readCancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = cli.Read(readCtx, fid, 0, 4096)
	}()

	time.Sleep(20 * time.Millisecond)
	readCancel()
	<-done

	srv.releaseRread() // drain server goroutine cleanly

	// Allow any late drops to finish.
	time.Sleep(50 * time.Millisecond)

	// Now issue a fresh Walk/Clunk cycle. Must succeed — if the tag
	// allocator leaked, FreeTagCount would be < 64 and a pathological
	// sequence of cancellations could eventually starve. For this
	// test, one follow-on op is sufficient.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	newFid := proto.Fid(2)
	if _, err := cli.Walk(ctx, proto.Fid(0), newFid, []string{"next.txt"}); err != nil {
		t.Fatalf("post-flush Walk: %v", err)
	}
	if err := cli.Clunk(ctx, newFid); err != nil {
		t.Fatalf("post-flush Clunk: %v", err)
	}
	if got := client.FreeTagCount(cli); got != 64 {
		t.Errorf("FreeTagCount after reuse = %d; want 64", got)
	}
}

// TestFlushAndWait_DoubleFlush_SingleFrame (Pitfall 1) asserts that
// repeated ctx.Cancel on an already-cancelled ctx does NOT produce
// multiple Tflush frames on the wire. The ctx is idempotent; each
// roundTrip sends at most one Tflush per ctx.Done fire.
func TestFlushAndWait_DoubleFlush_SingleFrame(t *testing.T) {
	t.Parallel()
	cli, srv, cleanup := newFlushTestPair(t)
	defer cleanup()

	fid := attachAndOpen(t, cli)

	srv.rflushSendImmediately.Store(true)

	readCtx, readCancel := context.WithCancel(t.Context())

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = cli.Read(readCtx, fid, 0, 4096)
	}()

	time.Sleep(20 * time.Millisecond)
	// Cancel THREE times. A well-behaved flushAndWait sends exactly one
	// Tflush — the repeated cancels are ctx-idempotent and have no
	// visible effect on the wire.
	readCancel()
	readCancel()
	readCancel()

	<-done
	srv.releaseRread()
	time.Sleep(50 * time.Millisecond)

	if got := srv.tflushCount.Load(); got != 1 {
		t.Errorf("tflushCount = %d after one Tread cancel; want exactly 1 (Pitfall 1)", got)
	}
}

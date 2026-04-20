package client

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
	"github.com/dotwaffle/ninep/proto/p9u"
	"github.com/dotwaffle/ninep/server"
	"github.com/dotwaffle/ninep/server/memfs"
)

// TestReadLoop_DispatchesRlerrorToRegisteredTag exercises the full
// register → read-loop-decode → inflight.deliver → receive path for a
// known R-message type. We Dial against a mock server (so we own the
// wire), then manually register a tag on the Conn's inflight, then have
// the mock server write a tagged Rlerror frame. The test receives on the
// registered respCh and asserts the type matches.
//
// Lives in the internal package (client, not client_test) so we can
// access the unexported inflight field and the codec decode path.
func TestReadLoop_DispatchesRlerrorToRegisteredTag(t *testing.T) {
	t.Parallel()

	cliNC, srvNC := net.Pipe()
	t.Cleanup(func() { _ = srvNC.Close() })

	// Drive Tversion handshake from the mock server side.
	go func() {
		var sizeBuf [4]byte
		if _, err := io.ReadFull(srvNC, sizeBuf[:]); err != nil {
			return
		}
		size := binary.LittleEndian.Uint32(sizeBuf[:])
		body := make([]byte, int(size)-4)
		if _, err := io.ReadFull(srvNC, body); err != nil {
			return
		}
		_ = p9l.Encode(srvNC, proto.NoTag, &proto.Rversion{Msize: 65536, Version: "9P2000.L"})
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	cli, err := Dial(ctx, cliNC, WithMsize(65536))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	// Register tag=42 on the client's inflight.
	tag := proto.Tag(42)
	respCh := cli.inflight.register(tag)

	// Mock server writes an Rlerror on tag 42.
	if err := p9l.Encode(srvNC, tag, &p9l.Rlerror{Ecode: 13 /* EACCES */}); err != nil {
		t.Fatalf("encode Rlerror: %v", err)
	}

	// Await the response on the caller side.
	select {
	case msg, ok := <-respCh:
		if !ok {
			t.Fatal("respCh closed before delivery")
		}
		rle, ok := msg.(*p9l.Rlerror)
		if !ok {
			t.Fatalf("received %T, want *p9l.Rlerror", msg)
		}
		if rle.Ecode != 13 {
			t.Fatalf("Rlerror.Ecode = %d, want 13", rle.Ecode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("respCh did not receive Rlerror within 2s")
	}
}

// TestReadLoop_DispatchesRwalkAndRclunkOutOfOrder: register tags 3 and 9,
// have the mock server write Rwalk for 9 then Rclunk for 3 (out of
// order), and assert each tag's respCh gets its own response.
func TestReadLoop_DispatchesRwalkAndRclunkOutOfOrder(t *testing.T) {
	t.Parallel()

	cliNC, srvNC := net.Pipe()
	t.Cleanup(func() { _ = srvNC.Close() })

	go func() {
		var sizeBuf [4]byte
		if _, err := io.ReadFull(srvNC, sizeBuf[:]); err != nil {
			return
		}
		size := binary.LittleEndian.Uint32(sizeBuf[:])
		body := make([]byte, int(size)-4)
		if _, err := io.ReadFull(srvNC, body); err != nil {
			return
		}
		_ = p9l.Encode(srvNC, proto.NoTag, &proto.Rversion{Msize: 65536, Version: "9P2000.L"})
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	cli, err := Dial(ctx, cliNC, WithMsize(65536))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	ch3 := cli.inflight.register(proto.Tag(3))
	ch9 := cli.inflight.register(proto.Tag(9))

	// Server writes Rwalk-tag=9 then Rclunk-tag=3.
	if err := p9l.Encode(srvNC, proto.Tag(9), &proto.Rwalk{QIDs: nil}); err != nil {
		t.Fatalf("encode Rwalk: %v", err)
	}
	if err := p9l.Encode(srvNC, proto.Tag(3), &proto.Rclunk{}); err != nil {
		t.Fatalf("encode Rclunk: %v", err)
	}

	select {
	case msg := <-ch9:
		if _, ok := msg.(*proto.Rwalk); !ok {
			t.Fatalf("ch9 got %T, want *proto.Rwalk", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ch9 (Rwalk) not received")
	}
	select {
	case msg := <-ch3:
		if _, ok := msg.(*proto.Rclunk); !ok {
			t.Fatalf("ch3 got %T, want *proto.Rclunk", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ch3 (Rclunk) not received")
	}
}

// TestReadLoop_UsesBytesReaderReset sanity-checks the bytes.Reader reuse:
// dispatch 50 frames, confirm all are delivered, and no per-frame alloc
// behaviour regresses. This isn't a true alloc benchmark (that lives in
// Plan 24) but it exercises the Reset call path under -race.
func TestReadLoop_UsesBytesReaderReset(t *testing.T) {
	t.Parallel()

	cliNC, srvNC := net.Pipe()
	t.Cleanup(func() { _ = srvNC.Close() })

	go func() {
		var sizeBuf [4]byte
		if _, err := io.ReadFull(srvNC, sizeBuf[:]); err != nil {
			return
		}
		size := binary.LittleEndian.Uint32(sizeBuf[:])
		body := make([]byte, int(size)-4)
		if _, err := io.ReadFull(srvNC, body); err != nil {
			return
		}
		_ = p9l.Encode(srvNC, proto.NoTag, &proto.Rversion{Msize: 65536, Version: "9P2000.L"})
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	cli, err := Dial(ctx, cliNC, WithMsize(65536))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	const N = 50
	channels := make([]chan proto.Message, N)
	for i := range N {
		channels[i] = cli.inflight.register(proto.Tag(i + 100))
	}

	// Encode all Rclunks into a buffer, write once.
	var frames bytes.Buffer
	for i := range N {
		if err := p9l.Encode(&frames, proto.Tag(i+100), &proto.Rclunk{}); err != nil {
			t.Fatalf("encode Rclunk: %v", err)
		}
	}
	go func() {
		_, _ = srvNC.Write(frames.Bytes())
	}()

	for i := range N {
		select {
		case msg := <-channels[i]:
			if _, ok := msg.(*proto.Rclunk); !ok {
				t.Fatalf("channels[%d] got %T, want *proto.Rclunk", i, msg)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("channels[%d] not received within 5s", i)
		}
	}
}

// TestNewRMessage_Phase21_DialectL_All asserts that every new .L R-type
// introduced by Phase 21 decodes to the correct *p9l.R<x> pointer on a
// protocolL-negotiated Conn. The newGateConn helper assembles a *Conn
// without a live wire; newRMessage only consults c.dialect.
func TestNewRMessage_Phase21_DialectL_All(t *testing.T) {
	t.Parallel()
	c := newGateConn(t, protocolL)

	cases := []struct {
		name    string
		msgType proto.MessageType
		assert  func(t *testing.T, msg proto.Message)
	}{
		{"Rgetattr", proto.TypeRgetattr, func(t *testing.T, msg proto.Message) {
			if _, ok := msg.(*p9l.Rgetattr); !ok {
				t.Fatalf("got %T, want *p9l.Rgetattr", msg)
			}
		}},
		{"Rsetattr", proto.TypeRsetattr, func(t *testing.T, msg proto.Message) {
			if _, ok := msg.(*p9l.Rsetattr); !ok {
				t.Fatalf("got %T, want *p9l.Rsetattr", msg)
			}
		}},
		{"Rstatfs", proto.TypeRstatfs, func(t *testing.T, msg proto.Message) {
			if _, ok := msg.(*p9l.Rstatfs); !ok {
				t.Fatalf("got %T, want *p9l.Rstatfs", msg)
			}
		}},
		{"Rsymlink", proto.TypeRsymlink, func(t *testing.T, msg proto.Message) {
			if _, ok := msg.(*p9l.Rsymlink); !ok {
				t.Fatalf("got %T, want *p9l.Rsymlink", msg)
			}
		}},
		{"Rreadlink", proto.TypeRreadlink, func(t *testing.T, msg proto.Message) {
			if _, ok := msg.(*p9l.Rreadlink); !ok {
				t.Fatalf("got %T, want *p9l.Rreadlink", msg)
			}
		}},
		{"Rlock", proto.TypeRlock, func(t *testing.T, msg proto.Message) {
			if _, ok := msg.(*p9l.Rlock); !ok {
				t.Fatalf("got %T, want *p9l.Rlock", msg)
			}
		}},
		{"Rgetlock", proto.TypeRgetlock, func(t *testing.T, msg proto.Message) {
			if _, ok := msg.(*p9l.Rgetlock); !ok {
				t.Fatalf("got %T, want *p9l.Rgetlock", msg)
			}
		}},
		{"Rxattrwalk", proto.TypeRxattrwalk, func(t *testing.T, msg proto.Message) {
			if _, ok := msg.(*p9l.Rxattrwalk); !ok {
				t.Fatalf("got %T, want *p9l.Rxattrwalk", msg)
			}
		}},
		{"Rxattrcreate", proto.TypeRxattrcreate, func(t *testing.T, msg proto.Message) {
			if _, ok := msg.(*p9l.Rxattrcreate); !ok {
				t.Fatalf("got %T, want *p9l.Rxattrcreate", msg)
			}
		}},
		{"Rlink", proto.TypeRlink, func(t *testing.T, msg proto.Message) {
			if _, ok := msg.(*p9l.Rlink); !ok {
				t.Fatalf("got %T, want *p9l.Rlink", msg)
			}
		}},
		{"Rmknod", proto.TypeRmknod, func(t *testing.T, msg proto.Message) {
			if _, ok := msg.(*p9l.Rmknod); !ok {
				t.Fatalf("got %T, want *p9l.Rmknod", msg)
			}
		}},
		{"Rrename", proto.TypeRrename, func(t *testing.T, msg proto.Message) {
			if _, ok := msg.(*p9l.Rrename); !ok {
				t.Fatalf("got %T, want *p9l.Rrename", msg)
			}
		}},
		{"Rrenameat", proto.TypeRrenameat, func(t *testing.T, msg proto.Message) {
			if _, ok := msg.(*p9l.Rrenameat); !ok {
				t.Fatalf("got %T, want *p9l.Rrenameat", msg)
			}
		}},
		{"Runlinkat", proto.TypeRunlinkat, func(t *testing.T, msg proto.Message) {
			if _, ok := msg.(*p9l.Runlinkat); !ok {
				t.Fatalf("got %T, want *p9l.Runlinkat", msg)
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg, err := c.newRMessage(tc.msgType)
			if err != nil {
				t.Fatalf("newRMessage(%v) err = %v", tc.msgType, err)
			}
			if msg == nil {
				t.Fatalf("newRMessage(%v) returned nil", tc.msgType)
			}
			tc.assert(t, msg)
		})
	}
}

// TestNewRMessage_Phase21_DialectU_All asserts .u-only R-types decode to
// their *p9u.R<x> concrete pointers on a protocolU-negotiated Conn.
func TestNewRMessage_Phase21_DialectU_All(t *testing.T) {
	t.Parallel()
	c := newGateConn(t, protocolU)

	cases := []struct {
		name    string
		msgType proto.MessageType
		assert  func(t *testing.T, msg proto.Message)
	}{
		{"Rstat", proto.TypeRstat, func(t *testing.T, msg proto.Message) {
			if _, ok := msg.(*p9u.Rstat); !ok {
				t.Fatalf("got %T, want *p9u.Rstat", msg)
			}
		}},
		{"Rwstat", proto.TypeRwstat, func(t *testing.T, msg proto.Message) {
			if _, ok := msg.(*p9u.Rwstat); !ok {
				t.Fatalf("got %T, want *p9u.Rwstat", msg)
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg, err := c.newRMessage(tc.msgType)
			if err != nil {
				t.Fatalf("newRMessage(%v) err = %v", tc.msgType, err)
			}
			if msg == nil {
				t.Fatalf("newRMessage(%v) returned nil", tc.msgType)
			}
			tc.assert(t, msg)
		})
	}
}

// TestNewRMessage_Phase21_CrossDialect_Rejects confirms defense-in-depth:
// on a protocolL Conn, a .u-only R-type returns error (dropped into the
// default arm). Same for protocolU + a .L-only R-type. A malicious peer
// emitting cross-dialect traffic triggers signalShutdown in readLoop, not
// a decode-misalignment crash.
func TestNewRMessage_Phase21_CrossDialect_Rejects(t *testing.T) {
	t.Parallel()

	cL := newGateConn(t, protocolL)
	// Rstat and Rwstat are .u-only — must error on .L.
	for _, mt := range []proto.MessageType{proto.TypeRstat, proto.TypeRwstat} {
		if msg, err := cL.newRMessage(mt); err == nil {
			t.Errorf("newRMessage(%v) on .L: got %T + nil err, want error", mt, msg)
		}
	}

	cU := newGateConn(t, protocolU)
	// All .L-only R-types — must error on .u.
	lOnly := []proto.MessageType{
		proto.TypeRgetattr,
		proto.TypeRsetattr,
		proto.TypeRstatfs,
		proto.TypeRsymlink,
		proto.TypeRreadlink,
		proto.TypeRlock,
		proto.TypeRgetlock,
		proto.TypeRxattrwalk,
		proto.TypeRxattrcreate,
		proto.TypeRlink,
		proto.TypeRmknod,
		proto.TypeRrename,
		proto.TypeRrenameat,
		proto.TypeRunlinkat,
	}
	for _, mt := range lOnly {
		if msg, err := cU.newRMessage(mt); err == nil {
			t.Errorf("newRMessage(%v) on .u: got %T + nil err, want error", mt, msg)
		}
	}
}

// TestNewRMessage_Phase21_Rstat_On_L_ReturnsError is the explicit,
// single-case assertion that a .u stat type on a .L Conn never decodes —
// a duplicate of the cross-dialect table but called out independently
// because the read-loop misalignment risk for Rstat/Rwstat is the
// specific hazard the dialect gate protects against.
func TestNewRMessage_Phase21_Rstat_On_L_ReturnsError(t *testing.T) {
	t.Parallel()
	c := newGateConn(t, protocolL)
	if _, err := c.newRMessage(proto.TypeRstat); err == nil {
		t.Fatal("newRMessage(TypeRstat) on .L: want error, got nil")
	}
	if _, err := c.newRMessage(proto.TypeRwstat); err == nil {
		t.Fatal("newRMessage(TypeRwstat) on .L: want error, got nil")
	}
}

// dialMockL is the internal-test version of dialPairL: builds a (cli,
// srvNC) pair via net.Pipe + a tight Tversion exchange, returns the
// internal *Conn (not *client.Conn) so tests can poke unexported fields
// like inflight + closeCh.
func dialMockL(t *testing.T) (*Conn, net.Conn) {
	t.Helper()
	cliNC, srvNC := net.Pipe()
	t.Cleanup(func() { _ = srvNC.Close() })

	go func() {
		var sizeBuf [4]byte
		if _, err := io.ReadFull(srvNC, sizeBuf[:]); err != nil {
			return
		}
		size := binary.LittleEndian.Uint32(sizeBuf[:])
		body := make([]byte, int(size)-4)
		if _, err := io.ReadFull(srvNC, body); err != nil {
			return
		}
		_ = p9l.Encode(srvNC, proto.NoTag, &proto.Rversion{Msize: 65536, Version: "9P2000.L"})
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	cli, err := Dial(ctx, cliNC, WithMsize(65536))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli, srvNC
}

// writeRreadFrame hand-constructs and writes an Rread frame on srv with
// the given tag and payload. Bypasses p9l.Encode so tests can deliberately
// craft malformed frames (e.g. count > len(payload), count > caller dst)
// to exercise the zero-copy Pitfall 1 guard paths.
//
// frameOverride lets tests specify a count value DIFFERENT from
// len(payload) — for the short-dst hazard where the server lies about
// how many bytes it sent.
func writeRreadFrame(t *testing.T, srv net.Conn, tag proto.Tag, count uint32, payload []byte) {
	t.Helper()
	// Frame: size[4] + type[1] + tag[2] + count[4] + payload[len(payload)]
	bodyLen := 1 + 2 + 4 + len(payload)
	size := uint32(4 + bodyLen)
	frame := make([]byte, 4+bodyLen)
	binary.LittleEndian.PutUint32(frame[0:4], size)
	frame[4] = byte(proto.TypeRread)
	binary.LittleEndian.PutUint16(frame[5:7], uint16(tag))
	binary.LittleEndian.PutUint32(frame[7:11], count)
	copy(frame[11:], payload)
	if _, err := srv.Write(frame); err != nil {
		t.Fatalf("write Rread frame: %v", err)
	}
}

// TestReadAt_ZeroCopy_HappyPath is the foundational positive case: the
// caller registers a ZC entry with a 12-byte dst; the mock server writes
// a well-formed 12-byte Rread frame; the read loop's fast path must
// copy the payload into dst, set entry.n = 12, and deliver
// rreadSentinelOK to the caller's channel.
//
// This is the spec-of-record for the read loop's Rread fast path.
func TestReadAt_ZeroCopy_HappyPath(t *testing.T) {
	t.Parallel()
	cli, srv := dialMockL(t)

	tag := proto.Tag(7)
	dst := make([]byte, 12)
	for i := range dst {
		dst[i] = 0xFF // sentinel — must be overwritten by payload
	}
	entry := cli.inflight.registerZC(tag, dst)

	payload := []byte("hello world\n")
	writeRreadFrame(t, srv, tag, uint32(len(payload)), payload)

	select {
	case msg, ok := <-entry.ch:
		if !ok {
			t.Fatal("entry.ch closed before delivery")
		}
		if msg != rreadSentinelOK {
			t.Fatalf("got msg %T (%v), want rreadSentinelOK pointer-equal", msg, msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("zero-copy Rread not delivered within 2s")
	}

	if entry.n != len(payload) {
		t.Errorf("entry.n = %d, want %d", entry.n, len(payload))
	}
	if string(dst) != string(payload) {
		t.Errorf("dst = %q, want %q", dst, payload)
	}
}

// TestReadAt_ZeroCopy_ShortDst exercises Pitfall 1 (24-RESEARCH.md):
// the server returns Rread.count > len(caller's dst). The read loop's
// zero-copy branch MUST detect the oversize and signalShutdown rather
// than silently truncating into dst (or worse, writing past dst's end
// and corrupting adjacent memory).
//
// Verified by:
//  1. Registering a 4-byte dst.
//  2. Sending a hand-crafted Rread frame whose count claims 8 bytes
//     and whose body actually carries 8 bytes (frame is well-formed
//     but oversized for THIS caller).
//  3. Asserting the Conn transitions to shutdown (cli.closeCh fires)
//     within a bounded window.
//  4. Asserting dst is NOT corrupted (the read loop returns BEFORE the
//     copy when count > len(dst); only the sentinel-fill survives).
func TestReadAt_ZeroCopy_ShortDst(t *testing.T) {
	t.Parallel()
	cli, srv := dialMockL(t)

	tag := proto.Tag(11)
	dst := make([]byte, 4)
	for i := range dst {
		dst[i] = 0xAA
	}
	_ = cli.inflight.registerZC(tag, dst)

	// Server says "I sent 8 bytes" + actually sends 8 bytes.
	overflow := []byte("12345678")
	writeRreadFrame(t, srv, tag, uint32(len(overflow)), overflow)

	// Conn should detect count > len(dst) and shut down.
	select {
	case <-cli.closeCh:
		// Expected: read loop signalled shutdown.
	case <-time.After(2 * time.Second):
		t.Fatal("Conn did not shut down within 2s after oversized Rread")
	}

	// dst MUST NOT have been touched (Pitfall 1: never silently truncate).
	for i, b := range dst {
		if b != 0xAA {
			t.Errorf("dst[%d] = %#x, want 0xAA (untouched); read loop wrote into dst before shutting down", i, b)
		}
	}
}

// TestReadAt_ZeroCopy_PipeFallback verifies Pattern B is transport-
// agnostic: the zero-copy branch fires equally on net.Pipe (no writev,
// no msg-coalescing) as it does on AF_UNIX. The win is alloc elimination,
// not transport-specific writev — pipe must work end-to-end.
//
// Goes through the full File.ReadAt → readAtZeroCopy → read-loop fast
// path → real memfs server stack. Asserts both the byte content AND a
// trailing 0xFF sentinel survives unmodified (proves no overrun beyond
// the requested count).
func TestReadAt_ZeroCopy_PipeFallback(t *testing.T) {
	t.Parallel()
	pair := pipeFallbackPair(t)
	defer pair.cleanup()

	root, err := pair.cli.Attach(pair.ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = root.Close() }()

	f, err := pair.cli.OpenFile(pair.ctx, "hello.txt", 0 /*O_RDONLY*/, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	const content = "hello world\n"
	const sentinelLen = 8
	dst := make([]byte, len(content)+sentinelLen)
	for i := range dst {
		dst[i] = 0xFF
	}

	n, err := f.ReadAt(dst[:len(content)], 0)
	if err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if n != len(content) {
		t.Errorf("ReadAt n=%d, want %d", n, len(content))
	}
	if string(dst[:n]) != content {
		t.Errorf("ReadAt content = %q, want %q", dst[:n], content)
	}
	// Trailing sentinel: read loop MUST NOT have written past dst[:len(content)].
	for i := len(content); i < len(dst); i++ {
		if dst[i] != 0xFF {
			t.Errorf("dst[%d] = %#x, want 0xFF (overrun beyond requested count)", i, dst[i])
		}
	}
}

// TestReadAt_ZeroCopy_CancelRace verifies Pitfall 2: ctx cancel during
// a ReadAt does not corrupt dst, regardless of whether Rread or Rflush
// wins the first-frame race. Under Pattern B the entire response body
// is received before the caller's select runs, so dst is either
// fully written or untouched — there is no mid-write cancel window.
//
// Stress: 100 sequential ReadAt iterations on independent Files,
// each with a per-iter random-microsecond cancel deadline. Verifies
// no panic + no -race report. Does NOT assert specific success/failure
// counts because the race outcome is intentionally non-deterministic
// (some iters complete normally if Rread wins; some return ErrFlushed
// if Rflush wins; some race the ctx).
func TestReadAt_ZeroCopy_CancelRace(t *testing.T) {
	t.Parallel()
	pair := pipeFallbackPair(t)
	defer pair.cleanup()

	root, err := pair.cli.Attach(pair.ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = root.Close() }()

	const iters = 100
	const content = "hello world\n"
	for i := range iters {
		f, err := pair.cli.OpenFile(pair.ctx, "hello.txt", 0 /*O_RDONLY*/, 0)
		if err != nil {
			t.Fatalf("iter %d: OpenFile: %v", i, err)
		}

		// Per-iter ctx with a random nanosecond deadline (0–500µs) — some
		// iters fire before the round trip starts (instant cancel), some
		// after the read loop delivers (no cancel triggered).
		ctx, cancel := context.WithTimeout(pair.ctx, time.Duration(i*5)*time.Microsecond)

		dst := make([]byte, len(content))
		// Pre-fill with sentinel; assertion below verifies no partial-write.
		for j := range dst {
			dst[j] = 0xCC
		}
		n, err := f.ReadAtCtx(ctx, dst, 0)
		cancel()

		// Acceptance: either complete success (n == len, err == nil, dst == content)
		// or full failure (n == 0, err != nil, dst untouched).
		// What is NOT acceptable: partial dst write paired with err != nil
		// (the Pattern B contract is all-or-nothing per round trip).
		if err == nil {
			if n != len(content) {
				t.Errorf("iter %d: nil err but n=%d, want %d", i, n, len(content))
			}
			if string(dst) != content {
				t.Errorf("iter %d: nil err but dst=%q, want %q", i, dst, content)
			}
		} else {
			// err != nil under Pattern B: dst must either be fully
			// untouched (sentinel preserved — cancel beat the read loop's
			// copy) OR fully written with content (the read loop won the
			// copy race but Go's select picked the ctx.Done arm of
			// readAtZeroCopy non-deterministically — a Pattern B win
			// observable as a "flushed" error).
			//
			// readAtZeroCopy returns (0, ferr) on the ctx.Done arm even
			// when dst was fully written, because the response chan was
			// not read by the time select fired. That's correct contract
			// behavior — the caller asked to cancel, and got a cancel
			// error — but it means n is 0 even when dst is full content.
			allSentinel := true
			for _, b := range dst {
				if b != 0xCC {
					allSentinel = false
					break
				}
			}
			allContent := string(dst) == content
			if !allSentinel && !allContent {
				t.Errorf("iter %d: err=%v but dst is neither all-sentinel nor full content: %q (n=%d)",
					i, err, dst, n)
			}
			// Partial writes are NEVER acceptable under Pattern B —
			// the read loop either copies the entire payload into dst
			// or doesn't touch dst at all.
			if !allSentinel && !allContent {
				t.Errorf("iter %d: PARTIAL DST WRITE: dst=%q (n=%d) — Pattern B contract violated",
					i, dst, n)
			}
		}
		_ = f.Close()
	}
}

// TestReadAt_ZeroCopy_CloseMidCopy_Race exercises WR-01: the data race
// where Conn.Close → signalShutdown → cancelAll closes entry.ch while
// the read loop's Rread fast path is mid-copy into entry.dst. The fix
// holds the inflight RLock across the lookup + copy + n + send so that
// cancelAll's Lock either runs before lookup (entry already gone, copy
// never happens) or after the send (caller wakes from a delivered
// sentinel, not from a closed-channel receive).
//
// Stress shape: many iterations, each registering a fresh ZC entry,
// having the mock server write a well-formed Rread frame, then racing
// a Close against the read loop's copy. Verifies:
//
//   - No -race detector report (the load-bearing assertion).
//   - dst is NEVER partially written: it's either all-sentinel (the read
//     loop never started the copy because the entry was gone) OR the
//     full payload (the read loop's RLock-spanned copy completed atomically).
//
// Without the WR-01 fix, partial writes are observable when the caller
// returns from <-entry.ch on the !ok arm (cancelAll closed it) before
// the read loop's copy finishes. The dst sentinel-vs-content check
// catches partial writes; the -race detector catches the unsynchronized
// access on dst's backing array.
func TestReadAt_ZeroCopy_CloseMidCopy_Race(t *testing.T) {
	t.Parallel()

	const iters = 50
	const sentinelByte = 0xCC
	payload := []byte("hello world\n")

	for i := range iters {
		// Per-iter Conn so Close is the natural teardown signal; no
		// shared state across iters keeps the race surface clean.
		cliNC, srvNC := net.Pipe()
		go func() {
			var sizeBuf [4]byte
			if _, err := io.ReadFull(srvNC, sizeBuf[:]); err != nil {
				return
			}
			size := binary.LittleEndian.Uint32(sizeBuf[:])
			body := make([]byte, int(size)-4)
			if _, err := io.ReadFull(srvNC, body); err != nil {
				return
			}
			_ = p9l.Encode(srvNC, proto.NoTag, &proto.Rversion{Msize: 65536, Version: "9P2000.L"})
		}()

		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		cli, err := Dial(ctx, cliNC, WithMsize(65536))
		cancel()
		if err != nil {
			_ = srvNC.Close()
			t.Fatalf("iter %d: Dial: %v", i, err)
		}

		tag := proto.Tag(1000 + i)
		dst := make([]byte, len(payload))
		for j := range dst {
			dst[j] = sentinelByte
		}
		entry := cli.inflight.registerZC(tag, dst)

		// Prime the wire with the Rread frame in a goroutine so the
		// read loop's copy can race with the Close below. Synchronous
		// net.Pipe writes block until the read loop reads, so the
		// goroutine is required. A failed write here is EXPECTED when
		// Close wins the race (cliNC is closed before the frame lands)
		// — tolerate via a local builder that doesn't t.Fatalf.
		writeDone := make(chan struct{})
		go func() {
			defer close(writeDone)
			bodyLen := 1 + 2 + 4 + len(payload)
			size := uint32(4 + bodyLen)
			frame := make([]byte, 4+bodyLen)
			binary.LittleEndian.PutUint32(frame[0:4], size)
			frame[4] = byte(proto.TypeRread)
			binary.LittleEndian.PutUint16(frame[5:7], uint16(tag))
			binary.LittleEndian.PutUint32(frame[7:11], uint32(len(payload)))
			copy(frame[11:], payload)
			_, _ = srvNC.Write(frame) // ignore EPIPE on Close-wins races
		}()

		// Race: Close concurrently with the read loop's copy. Whichever
		// wins, dst must be consistent (all-sentinel or all-payload),
		// and -race must not report a write-after-free.
		closeDone := make(chan struct{})
		go func() {
			defer close(closeDone)
			_ = cli.Close()
		}()

		// Drain the entry.ch arm or the cancelAll close-arm — both are
		// terminal for this round trip. We don't assert the outcome
		// because the race winner is intentionally non-deterministic.
		select {
		case <-entry.ch:
			// Either rreadSentinelOK (read loop won) or zero/closed
			// (cancelAll won). Either is acceptable.
		case <-time.After(2 * time.Second):
			t.Fatalf("iter %d: entry.ch never delivered or closed", i)
		}

		<-closeDone
		<-writeDone
		_ = srvNC.Close()

		// Acceptance: dst is either all-sentinel (cancelAll won the
		// race; copy never happened) OR full payload (read loop's
		// RLock-spanned copy completed). Partial writes are a
		// WR-01-regression signal.
		allSentinel := true
		for _, b := range dst {
			if b != sentinelByte {
				allSentinel = false
				break
			}
		}
		allPayload := bytes.Equal(dst, payload)
		if !allSentinel && !allPayload {
			t.Errorf("iter %d: PARTIAL DST WRITE (WR-01 regression): dst=%q, want all-sentinel or all-payload", i, dst)
		}
	}
}

// pipeFallbackPair is a tiny test fixture for the two pipe-based ZC tests
// above. Builds a real memfs server with a known "hello.txt" fixture and
// dials a client over net.Pipe. Lives in the internal test package so
// it has access to unexported *Conn methods if needed; constructs the
// public *client.Conn equivalent via the same Dial path the external
// helpers use.
type zcPipePair struct {
	cli     *Conn
	cleanup func()
	ctx     context.Context
}

func pipeFallbackPair(t *testing.T) *zcPipePair {
	t.Helper()
	root := buildZCTestRoot(t)
	cliNC, srvNC := net.Pipe()

	srv := newZCTestServer(t, root)
	srvCtx, srvCancel := context.WithTimeout(t.Context(), 30*time.Second)
	srvDone := make(chan struct{})
	go func() {
		defer close(srvDone)
		srv.ServeConn(srvCtx, srvNC)
	}()

	dialCtx, dialCancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer dialCancel()
	cli, err := Dial(dialCtx, cliNC, WithMsize(65536))
	if err != nil {
		_ = cliNC.Close()
		srvCancel()
		<-srvDone
		t.Fatalf("Dial: %v", err)
	}

	tCtx, tCancel := context.WithTimeout(t.Context(), 10*time.Second)
	cleanup := func() {
		tCancel()
		_ = cli.Close()
		srvCancel()
		_ = srvNC.Close()
		<-srvDone
	}
	return &zcPipePair{cli: cli, cleanup: cleanup, ctx: tCtx}
}

// buildZCTestRoot constructs a small memfs tree with a known "hello.txt"
// fixture for the zero-copy hazard tests. Mirrors the public-test fixture
// pattern in client/pair_test.go:buildTestRoot but lives in the internal
// package so dialMockL + pipeFallbackPair don't need cross-package
// helpers.
func buildZCTestRoot(tb testing.TB) server.Node {
	tb.Helper()
	gen := &server.QIDGenerator{}
	return memfs.NewDir(gen).AddStaticFile("hello.txt", "hello world\n")
}

// newZCTestServer wraps server.New for the ZC tests. Discards logs so
// the -v output stays focused on test signal.
func newZCTestServer(tb testing.TB, root server.Node) *server.Server {
	tb.Helper()
	return server.New(root,
		server.WithMaxMsize(65536),
		server.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
}

package client

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
	"github.com/dotwaffle/ninep/proto/p9u"
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
	for i := 0; i < N; i++ {
		channels[i] = cli.inflight.register(proto.Tag(i + 100))
	}

	// Encode all Rclunks into a buffer, write once.
	var frames bytes.Buffer
	for i := 0; i < N; i++ {
		if err := p9l.Encode(&frames, proto.Tag(i+100), &proto.Rclunk{}); err != nil {
			t.Fatalf("encode Rclunk: %v", err)
		}
	}
	go func() {
		_, _ = srvNC.Write(frames.Bytes())
	}()

	for i := 0; i < N; i++ {
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

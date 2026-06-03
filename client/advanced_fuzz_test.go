package client_test

// Stateful, 9P2000.L-aware fuzzing of the client's multi-phase operations.
//
// FuzzConnReadLoop (client package) feeds raw bytes into a passive read
// loop. These targets go further: they drive a real File.Lock or
// File.XattrGet against a scripted server that answers each T-message with
// a fuzz-chosen reply, so the client's poll/retry and two-phase state
// machines run under adversarial but structurally-plausible responses.
//
// Oracle: the operation must not panic and must not deadlock. A watchdog
// fails the input if an op fails to return well after its context expires.
// Throughput is kept high by a zero lock-poll backoff and by closing the
// connection once the fuzz script is exhausted, which bounds every op even
// without relying on the context deadline.

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// fuzzMsize is small enough that a multi-hundred-byte xattr drains over
// several Tread round-trips, exercising the loop, yet above Dial's
// minMsize floor (256) so negotiation succeeds.
const fuzzMsize = 4096

// cursor doles out the fuzz input one byte at a time. Once exhausted it
// reports done so the scripted server can close the connection, which
// bounds the client operation.
type cursor struct {
	b    []byte
	i    int
	done bool
}

func (c *cursor) next() byte {
	if c.i >= len(c.b) {
		c.done = true
		return 0
	}
	v := c.b[c.i]
	c.i++
	return v
}

// serveScript answers the Tversion handshake with a fixed 9P2000.L
// Rversion, then replies to each subsequent T-message with a fuzz-chosen
// response until the script runs out or a reply tears down the stream.
func serveScript(srvNC net.Conn, cur *cursor) {
	// Close on exit so the client's pending or next I/O unblocks with
	// ErrClosedPipe rather than wedging on a peerless net.Pipe. This is
	// what bounds every client op once the script is spent.
	defer func() { _ = srvNC.Close() }()
	if !consumeFrame(srvNC) { // consume the client's Tversion frame
		return
	}
	rver := &proto.Rversion{Msize: fuzzMsize, Version: "9P2000.L"}
	if err := p9l.Encode(srvNC, proto.NoTag, rver); err != nil {
		return
	}
	for {
		tag, msg, err := p9l.Decode(srvNC)
		if err != nil {
			return
		}
		if cur.done || respond(srvNC, tag, msg, cur) || cur.done {
			return
		}
	}
}

// consumeFrame reads and discards one length-prefixed 9P frame. Returns
// false on a short or malformed length.
func consumeFrame(r net.Conn) bool {
	var sizeBuf [4]byte
	if _, err := io.ReadFull(r, sizeBuf[:]); err != nil {
		return false
	}
	size := binary.LittleEndian.Uint32(sizeBuf[:])
	if size < 4 {
		return false
	}
	_, err := io.ReadFull(r, make([]byte, int(size)-4))
	return err == nil
}

// respond writes one fuzz-chosen reply for the decoded T-message. It
// returns true when the reply (or a write error) should end the server
// loop and close the connection.
func respond(w net.Conn, tag proto.Tag, msg proto.Message, cur *cursor) (closeAfter bool) {
	switch msg.(type) {
	case *p9l.Tlock:
		switch cur.next() % 6 {
		case 0:
			return enc(w, tag, &p9l.Rlock{Status: proto.LockStatusOK})
		case 1:
			return enc(w, tag, &p9l.Rlock{Status: proto.LockStatusBlocked})
		case 2:
			return enc(w, tag, &p9l.Rlock{Status: proto.LockStatusGrace})
		case 3:
			// Arbitrary status byte, including values the client treats
			// as Error or as an unknown status.
			return enc(w, tag, &p9l.Rlock{Status: proto.LockStatus(cur.next())})
		case 4:
			return enc(w, tag, &p9l.Rlerror{Ecode: proto.Errno(cur.next())})
		default:
			return perturb(w, cur)
		}
	case *p9l.Txattrwalk:
		switch cur.next() % 6 {
		case 0, 1, 2:
			return enc(w, tag, &p9l.Rxattrwalk{Size: xattrSize(cur)})
		case 3:
			return enc(w, tag, &p9l.Rlerror{Ecode: proto.Errno(cur.next())})
		default:
			return perturb(w, cur)
		}
	case *proto.Tread:
		switch cur.next() % 6 {
		case 0, 1, 2:
			return enc(w, tag, &proto.Rread{Data: readData(cur)})
		case 3:
			return enc(w, tag, &p9l.Rlerror{Ecode: proto.Errno(cur.next())})
		default:
			return perturb(w, cur)
		}
	case *proto.Tclunk:
		switch cur.next() % 4 {
		case 0, 1:
			return enc(w, tag, &proto.Rclunk{})
		case 2:
			return enc(w, tag, &p9l.Rlerror{Ecode: proto.Errno(cur.next())})
		default:
			return perturb(w, cur)
		}
	default:
		return enc(w, tag, &p9l.Rlerror{Ecode: proto.ENOSYS})
	}
}

func enc(w net.Conn, tag proto.Tag, msg proto.Message) (closeAfter bool) {
	return p9l.Encode(w, tag, msg) != nil
}

// xattrSize picks a server-declared xattr length: zero (short-circuit), a
// small size, a value just over MaxDataSize (the client must reject it
// before allocating), or a few KB that drains over multiple reads. It
// never returns a size near MaxDataSize, which would force a huge
// allocation per fuzz iteration.
func xattrSize(cur *cursor) uint64 {
	switch cur.next() % 4 {
	case 0:
		return 0
	case 1:
		return uint64(cur.next())
	case 2:
		return uint64(proto.MaxDataSize) + 1
	default:
		return uint64(cur.next())%8192 + 1
	}
}

// readData returns 0..63 bytes, including the zero-length short-read case
// and lengths that may exceed the client's remaining request.
func readData(cur *cursor) []byte {
	n := int(cur.next()) % 64
	d := make([]byte, n)
	for i := range d {
		d[i] = cur.next()
	}
	return d
}

// perturb emits a structural perturbation (mismatched tag or garbage
// frame) and then signals the server to close, so the read loop's
// drop/decode-error paths run and the pending op is bounded by the close.
func perturb(w net.Conn, cur *cursor) (closeAfter bool) {
	switch cur.next() % 3 {
	case 0:
		// Valid frame, mismatched tag: the read loop must drop it without
		// matching the inflight op.
		_ = p9l.Encode(w, proto.NoTag, &p9l.Rlock{Status: proto.LockStatusOK})
	case 1:
		// Garbage frame (unknown type, 9-byte length): the decode must
		// fail without panicking.
		_, _ = w.Write([]byte{0x09, 0x00, 0x00, 0x00, 0xEE, 0x34, 0x12, 0xAB, 0xCD})
	}
	return true
}

// fuzzDial wires a client to a scripted server over net.Pipe with a zero
// lock backoff so blocked-lock polling spins without real sleeps.
func fuzzDial(t *testing.T, data []byte) (*client.Conn, func()) {
	t.Helper()
	cliNC, srvNC := net.Pipe()
	srvDone := make(chan struct{})
	go func() {
		defer close(srvDone)
		serveScript(srvNC, &cursor{b: data})
	}()

	dialCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, err := client.Dial(dialCtx, cliNC,
		client.WithMsize(fuzzMsize),
		client.WithLockPollSchedule([]time.Duration{0}),
		client.WithLogger(discardLogger()),
	)
	if err != nil {
		_ = cliNC.Close()
		_ = srvNC.Close()
		<-srvDone
		// The server always sends a valid Rversion, so a Dial failure is
		// not a finding; skip this input.
		t.Skipf("dial: %v", err)
	}
	cleanup := func() {
		_ = c.Close()
		_ = cliNC.Close()
		_ = srvNC.Close()
		<-srvDone
	}
	return c, cleanup
}

// runOp runs op under a short context and fails the input if it does not
// return well after the context expires (a deadlock).
func runOp(t *testing.T, op func(context.Context)) {
	t.Helper()
	opCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		op(opCtx)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("operation deadlocked: no return within 3s despite a 100ms ctx")
	}
}

// FuzzClientLock drives File.Lock against fuzz-scripted Rlock/Rlerror
// sequences, exercising the poll/backoff/ctx-cancel state machine.
func FuzzClientLock(f *testing.F) {
	f.Add([]byte{0})       // immediate OK
	f.Add([]byte{1, 0})    // BLOCKED then OK
	f.Add([]byte{4, 13})   // Rlerror EACCES
	f.Add([]byte{1, 1, 1}) // repeated BLOCKED, then script-exhaust close

	f.Fuzz(func(t *testing.T, data []byte) {
		c, cleanup := fuzzDial(t, data)
		defer cleanup()
		file := client.NewFileWrappingFidForTest(c, proto.Fid(1), 0)
		runOp(t, func(ctx context.Context) { _ = file.Lock(ctx, client.LockWrite) })
	})
}

// FuzzClientXattr drives File.XattrGet against fuzz-scripted Rxattrwalk +
// Tread-drain + Tclunk sequences, exercising the two-phase read path.
func FuzzClientXattr(f *testing.F) {
	f.Add([]byte{0, 0, 0})                         // zero-size: short-circuit + clunk
	f.Add([]byte{0, 1, 5, 0, 5, 1, 2, 3, 4, 5, 0}) // small read then clunk
	f.Add([]byte{3})                               // Rlerror on Txattrwalk

	f.Fuzz(func(t *testing.T, data []byte) {
		c, cleanup := fuzzDial(t, data)
		defer cleanup()
		file := client.NewFileWrappingFidForTest(c, proto.Fid(1), 0)
		runOp(t, func(ctx context.Context) { _, _ = file.XattrGet(ctx, "user.test") })
	})
}

// Package wire provides the 4-byte size-prefix framing and net.Buffers
// writev glue for 9P wire messages. It is extracted from server/conn.go's
// handleRequest read path and sendResponseInline write path so the client
// library (Phase 19+) can share a single implementation.
//
// # Shape
//
// All three exports are package-level functions with no state. Callers own
// their mutex (if any), their reusable buffers, and their net.Conn read
// and write deadlines. Deadlines are NOT threaded through these helpers;
// callers set them on the net.Conn directly before calling in.
//
// A Framer struct was rejected during Phase 18 planning (D-07) because the
// 1%-of-sec-op benchmark gate is tight enough that an extra indirection
// could move the measurement. Stay close to the inline shape — pass args,
// return errors, nothing else.
//
// # Read path
//
// The read path is split into [ReadSize] and [ReadBody] rather than a single
// "ReadFrame" helper. The server must validate frame size against its
// negotiated msize between the two reads (otherwise an attacker's 4 GiB size
// field would cause the body buffer to be allocated before msize is
// consulted). Splitting also keeps the helpers policy-free: ReadSize and
// ReadBody do not know or care about msize.
//
// # Write path
//
// [WriteFramesLocked] delegates to net.Buffers.WriteTo, which on Linux TCP
// and unix-domain sockets issues a single writev syscall. The *Locked suffix
// is a naming convention: the caller holds whatever mutex serialises writes
// on the underlying net.Conn. Go has no lock-ownership type system, so this
// is honour-code enforced.
//
// # net.Buffers.WriteTo consume invariant
//
// The stdlib's net.Buffers.WriteTo mutates its receiver via v.consume, which
// on full consumption sets both length AND capacity of the backing slice to
// zero. Callers MUST re-slice the net.Buffers value from a conn-resident
// backing array on every call. WriteFramesLocked accepts *net.Buffers
// (pointer) rather than net.Buffers (value) to surface this semantic at the
// call signature — the reader of a call site can see the bufs will be
// mutated. See ninep CLAUDE.md §Performance.
package wire

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"

	"github.com/dotwaffle/ninep/proto"
)

// ReadSize reads the 4-byte little-endian size prefix of one 9P frame from
// r. It returns the encoded frame size (including the 4 header bytes); the
// caller computes body length as size - 4.
//
// Callers must validate size against any transport-specific upper bound
// (e.g., the negotiated msize) BEFORE allocating a body buffer and calling
// [ReadBody]. This helper is deliberately policy-free.
//
// A size smaller than [proto.HeaderSize] (7 bytes: size[4] + type[1] +
// tag[2]) is returned as a descriptive error; such a frame is fatal and the
// caller should enter its shutdown path. Short reads of the prefix itself
// surface as io.EOF (zero bytes read) or io.ErrUnexpectedEOF (partial read)
// via io.ReadFull; callers discriminate these to classify connection-close
// versus frame-corruption paths.
func ReadSize(r io.Reader) (uint32, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, err
	}
	size := binary.LittleEndian.Uint32(hdr[:])
	if size < proto.HeaderSize {
		return 0, fmt.Errorf("wire: frame size %d smaller than 9P header (%d)", size, proto.HeaderSize)
	}
	return size, nil
}

// ReadBody reads exactly len(buf) bytes into buf. The caller has already
// sliced buf to the desired body length (typically size - 4 from a preceding
// [ReadSize] call, with size validated against msize in between).
//
// ReadBody does not resize buf. Callers that source buf from a bucketed pool
// (e.g., internal/bufpool.GetMsgBuf) rely on this: the bucket invariant
// (slice length < bucket capacity; cap untouched on Put) must survive the
// read.
//
// Short reads surface as io.ErrUnexpectedEOF per io.ReadFull semantics.
// Callers discriminate io.EOF / io.ErrUnexpectedEOF / net.ErrClosed to
// classify connection-close versus frame-corruption paths.
func ReadBody(r io.Reader, buf []byte) error {
	_, err := io.ReadFull(r, buf)
	return err
}

// WriteFramesLocked writes every buffer in *bufs to w as a single writev
// syscall when w's underlying type supports it (net.TCPConn, net.UnixConn).
// On other writers (net.Pipe, bytes.Buffer in tests) it falls back to
// sequential Write calls; the 9P wire-level semantic (contiguous bytes in
// order) is identical.
//
// # Locking
//
// The *Locked suffix is a naming convention reminding callers that w's
// writes must be serialised by a mutex the caller holds. WriteFramesLocked
// itself takes and releases no locks. Mis-use (calling without holding the
// mutex) lets two writev calls interleave their iovecs on the wire,
// producing frames the remote end cannot parse.
//
// # *net.Buffers is mutated
//
// net.Buffers.WriteTo calls v.consume internally, which on full consumption
// sets both the length AND the capacity of the receiver slice to zero. The
// consumed bufs are therefore unusable for a subsequent WriteFramesLocked
// call: the caller MUST re-slice *bufs from a conn-resident backing array
// each time. Passing a *net.Buffers that was successfully consumed by a
// prior call will silently write zero bytes on the next call.
//
// The server's sendResponseInline re-slices c.encBufsArr[:n] on every
// response; the client will do the analogous thing on its outbound Tread /
// Twrite path in Phase 19.
func WriteFramesLocked(w io.Writer, bufs *net.Buffers) error {
	if _, err := bufs.WriteTo(w); err != nil {
		return fmt.Errorf("wire: writev: %w", err)
	}
	return nil
}

package client

import (
	"encoding/binary"
	"fmt"
	"net"

	"github.com/dotwaffle/ninep/internal/bufpool"
	"github.com/dotwaffle/ninep/internal/wire"
	"github.com/dotwaffle/ninep/proto"
)

// writeT encodes msg and writes the framed T-message under writeMu.
// Caller owns tag acquisition/release and inflight.register — writeT
// only does encode + writev.
//
// Mirrors server/conn.go's sendResponseInline 2-entry path (hdr + body).
// The 3-entry Payloader path is a server-only optimization for Rread;
// Phase 19 does not symmetrically optimize Twrite outbound (deferred to
// Phase 24 per CLIENT-06).
//
// Per Pitfall 7 + research §8 invariant 2: the net.Buffers slice is
// re-sliced from c.encBufsArr on EVERY call because
// net.Buffers.WriteTo's v.consume mutates both length AND capacity of
// the receiver on full consumption. Passing a hoisted net.Buffers field
// would silently write zero bytes after the first call.
//
// Returns ErrClosed wrapped via fmt.Errorf if the Conn has been
// signalShutdown'd before writeT is called. In-flight shutdowns during
// the write surface as whatever wire.WriteFramesLocked returns from the
// underlying net.Conn.
func (c *Conn) writeT(tag proto.Tag, msg proto.Message) error {
	// Cheap pre-flight — saves encoding a message we're about to drop.
	if c.isClosed() {
		return fmt.Errorf("client: writeT: %w", ErrClosed)
	}

	// Encode body OUTSIDE writeMu to keep the critical section short.
	// The body buffer is borrowed from the shared bufpool and returned
	// via defer after the Write completes synchronously.
	body := bufpool.GetBuf()
	defer bufpool.PutBuf(body)
	if err := msg.EncodeTo(body); err != nil {
		return fmt.Errorf("client: encode %s: %w", msg.Type(), err)
	}

	size := uint32(proto.HeaderSize) + uint32(body.Len())
	if size > c.msize {
		return fmt.Errorf("client: frame size %d exceeds negotiated msize %d", size, c.msize)
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	// Re-check closed-state under writeMu — if signalShutdown fired while
	// we were encoding, the writev below would fail with net.ErrClosed
	// anyway, but returning ErrClosed early is a cleaner error surface.
	if c.isClosed() {
		return fmt.Errorf("client: writeT: %w", ErrClosed)
	}

	// Fill header. Guarded by writeMu (c.encHdr is a conn-resident field
	// so there's no per-send heap alloc).
	binary.LittleEndian.PutUint32(c.encHdr[0:4], size)
	c.encHdr[4] = uint8(msg.Type())
	binary.LittleEndian.PutUint16(c.encHdr[5:7], uint16(tag))

	// Re-slice buffers from the conn-resident backing array on every
	// call (Pitfall 7 — net.Buffers.WriteTo consumes len AND cap).
	c.encBufsArr[0] = c.encHdr[:]
	c.encBufsArr[1] = body.Bytes()
	bufs := net.Buffers(c.encBufsArr[:2])

	if err := wire.WriteFramesLocked(c.nc, &bufs); err != nil {
		return fmt.Errorf("client: write %s: %w", msg.Type(), err)
	}
	return nil
}

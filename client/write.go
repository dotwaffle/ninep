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
// Pattern A (2-entry): Header + Pooled Body. Used for small control
// messages (Twalk, Tclunk, etc.).
//
// Pattern B (3-entry): Header + Pooled Fixed Fields + Uncopied Payload.
// Used for large writes (Twrite) when msg implements [proto.Payloader].
// This eliminates copying large user buffers into the pooled buffer
// (SC-4 mirror optimization).
//
// Per Pitfall 7 + research §8 invariant 2: the net.Buffers slice is
// re-sliced from c.encBufsArr on EVERY call because
// net.Buffers.WriteTo's v.consume mutates both length AND capacity of
// the receiver on full consumption. Passing a hoisted net.Buffers field
// would silently write zero bytes after the first call.
func (c *Conn) writeT(tag proto.Tag, msg proto.Message) error {
	if c.isClosed() {
		return fmt.Errorf("client: writeT: %w", ErrClosed)
	}

	body := bufpool.GetBuf()
	defer bufpool.PutBuf(body)

	var payload []byte
	var usePatternB bool
	if pl, ok := msg.(proto.Payloader); ok {
		usePatternB = true
		payload = pl.Payload()
		if err := pl.EncodeFixed(body); err != nil {
			return fmt.Errorf("client: encode %s (fixed): %w", msg.Type(), err)
		}
	} else {
		if err := msg.EncodeTo(body); err != nil {
			return fmt.Errorf("client: encode %s: %w", msg.Type(), err)
		}
	}

	size := uint32(proto.HeaderSize) + uint32(body.Len()) + uint32(len(payload))
	if size > c.msize {
		return fmt.Errorf("client: frame size %d exceeds negotiated msize %d", size, c.msize)
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if c.isClosed() {
		return fmt.Errorf("client: writeT: %w", ErrClosed)
	}

	binary.LittleEndian.PutUint32(c.encHdr[0:4], size)
	c.encHdr[4] = uint8(msg.Type())
	binary.LittleEndian.PutUint16(c.encHdr[5:7], uint16(tag))

	c.encBufsArr[0] = c.encHdr[:]
	c.encBufsArr[1] = body.Bytes()
	nBufs := 2
	if usePatternB && len(payload) > 0 {
		c.encBufsArr[2] = payload
		nBufs = 3
	}
	bufs := net.Buffers(c.encBufsArr[:nBufs])

	if err := wire.WriteFramesLocked(c.nc, &bufs); err != nil {
		return fmt.Errorf("client: write %s: %w", msg.Type(), err)
	}
	return nil
}

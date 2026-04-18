package client

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"

	"github.com/dotwaffle/ninep/internal/bufpool"
	"github.com/dotwaffle/ninep/internal/wire"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
	"github.com/dotwaffle/ninep/proto/p9u"
)

// readLoop is the per-Conn read goroutine. It reads one 9P frame at a
// time, decodes the header, allocates or pools an R-message struct via
// newRMessage, decodes the body, and delivers the decoded message (as a
// proto.Message interface value) to the registered inflight chan for the
// frame's tag.
//
// Chan-type contract (locked by this plan's objective + Task 1): the
// inflight chan is chan proto.Message (value). Because proto.Message is
// an interface holding a pointer-to-concrete-type, passing the decoded
// rmsg directly transports the underlying pointer at zero indirection.
//
// Mirror of server/conn.go's handleRequest hot loop but:
//
//   - reads R-messages (client inverts the server's T set)
//   - delivers to per-tag respCh (server dispatches to handlers)
//   - single goroutine (server has lazy-spawned worker pool behind recvMu)
//
// Exit conditions (any triggers signalShutdown):
//
//   - wire.ReadSize returns err (EOF, net.ErrClosed, io.ErrUnexpectedEOF)
//   - size > c.msize (oversize frame — Pitfall 10-B)
//   - wire.ReadBody returns err
//   - newRMessage(type) returns ErrUnknownType
//   - msg.DecodeFrom returns err
//
// Per Pitfall 10-B: a decode error is FATAL (the wire stream is now
// misaligned because we don't know how many bytes the malformed body
// should have consumed). Log + signalShutdown + return; we deliberately
// do NOT log-and-continue.
func (c *Conn) readLoop() {
	defer c.readerWG.Done()

	// Goroutine-local bytes.Reader to avoid per-frame Reader allocs
	// (Pitfall 4 — mirrors server/conn.go's hot-path pattern).
	var bodyReader bytes.Reader

	for {
		size, err := wire.ReadSize(c.nc)
		if err != nil {
			if !isClosedErr(err) {
				c.logger.Debug("client: read frame size",
					slog.Any("error", err),
				)
			}
			c.signalShutdown()
			return
		}

		// msize validation between ReadSize and ReadBody per
		// internal/wire contract (Phase 18 D-06 preserved) + research §1.
		if size > c.msize {
			c.logger.Warn("client: oversize frame",
				slog.Uint64("size", uint64(size)),
				slog.Uint64("msize", uint64(c.msize)),
			)
			c.signalShutdown()
			return
		}

		// Read body into a pooled bucket buffer. size - 4 is body length
		// (excluding the 4-byte size prefix already consumed).
		bodyLen := int(size) - 4
		bufPtr := bufpool.GetMsgBuf(bodyLen)
		b := (*bufPtr)[:bodyLen]
		if err := wire.ReadBody(c.nc, b); err != nil {
			bufpool.PutMsgBuf(bufPtr)
			if !isClosedErr(err) {
				c.logger.Debug("client: read frame body",
					slog.Any("error", err),
				)
			}
			c.signalShutdown()
			return
		}

		// Parse header fields from body: type[1] + tag[2].
		if len(b) < 3 {
			bufpool.PutMsgBuf(bufPtr)
			c.logger.Warn("client: frame body smaller than inner header",
				slog.Int("len", len(b)),
			)
			c.signalShutdown()
			return
		}
		msgType := proto.MessageType(b[0])
		tag := proto.Tag(binary.LittleEndian.Uint16(b[1:3]))

		rmsg, err := c.newRMessage(msgType)
		if err != nil {
			bufpool.PutMsgBuf(bufPtr)
			c.logger.Warn("client: unknown R-message type",
				slog.String("type", msgType.String()),
			)
			c.signalShutdown()
			return
		}

		bodyReader.Reset(b[3:]) // zero-alloc Reader reset (Pitfall 4)
		if err := rmsg.DecodeFrom(&bodyReader); err != nil {
			bufpool.PutMsgBuf(bufPtr)
			c.logger.Warn("client: decode R-message",
				slog.String("type", msgType.String()),
				slog.Any("error", err),
			)
			c.signalShutdown()
			return
		}
		bufpool.PutMsgBuf(bufPtr)

		// Deliver the proto.Message interface value (which already holds
		// a pointer-to-concrete-type). Chan type is chan proto.Message
		// per Task 1's inflightMap; no pointer-to-interface wrapping.
		c.inflight.deliver(tag, rmsg)
	}
}

// signalShutdown is safe to call multiple times (closeOnce). Closes
// closeCh, cancels all inflight callers, and closes nc so any peer
// blocked on read also exits. The full Close/Shutdown drain sequence
// lives in Plan 19-05; this helper just fires the signals.
func (c *Conn) signalShutdown() {
	c.closeOnce.Do(func() {
		close(c.closeCh)
		_ = c.nc.Close()
		c.inflight.cancelAll()
	})
}

// newRMessage returns a zero-reset pointer to the R-message struct for
// the given MessageType, drawn from client/msgcache.go's pool where the
// type is cached. Unknown or dialect-gated types return an error.
//
// Mirrors server/conn.go's newMessage but for R-messages and dialect-
// gated: protocolL Conns only accept .L-specific response types; same
// for protocolU. Rversion and Rflush are dialect-neutral. Rerror is
// .u-only on the wire (Rlerror is the .L analogue).
//
// The returned value is wrapped in proto.Message at the return site so
// readLoop's inflight.deliver gets an interface value holding a pointer
// to the concrete type.
func (c *Conn) newRMessage(t proto.MessageType) (proto.Message, error) {
	switch t {
	case proto.TypeRversion:
		// Cold path, once per Conn lifetime; not cached.
		return &proto.Rversion{}, nil
	case proto.TypeRattach:
		return &proto.Rattach{}, nil
	case proto.TypeRwalk:
		return getCachedRwalk(), nil
	case proto.TypeRread:
		return getCachedRread(), nil
	case proto.TypeRwrite:
		return getCachedRwrite(), nil
	case proto.TypeRclunk:
		return getCachedRclunk(), nil
	case proto.TypeRremove:
		return &proto.Rremove{}, nil
	case proto.TypeRflush:
		return &proto.Rflush{}, nil
	case proto.TypeRlerror:
		return getCachedRlerror(), nil
	case proto.TypeRlopen:
		if c.dialect == protocolL {
			return getCachedRlopen(), nil
		}
	case proto.TypeRlcreate:
		if c.dialect == protocolL {
			return getCachedRlcreate(), nil
		}
	case proto.TypeRopen:
		if c.dialect == protocolU {
			return &p9u.Ropen{}, nil
		}
	case proto.TypeRcreate:
		if c.dialect == protocolU {
			return &p9u.Rcreate{}, nil
		}
	case proto.TypeRerror:
		// .u-only on the wire, but we accept defensively; mis-dialect
		// traffic is a misbehaving server.
		return &p9u.Rerror{}, nil
	case proto.TypeRreaddir:
		// .L-only directory-enumeration response. Cold path relative
		// to Rread/Rwrite -- not cached per client/msgcache.go.
		if c.dialect == protocolL {
			return &p9l.Rreaddir{}, nil
		}
	}
	return nil, errors.New("client: unknown R-message type")
}

// isClosedErr reports whether err is from a closed net.Conn or an EOF path.
// Prevents spurious log lines during graceful shutdown.
func isClosedErr(err error) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.ErrClosedPipe)
}

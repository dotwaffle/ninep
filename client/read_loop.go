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

		// 24-03 / D-05: zero-copy Rread fast path (Pattern B from
		// 24-RESEARCH.md §Architecture Patterns).
		//
		// When Conn.readAtZeroCopy registered this tag with a non-nil dst
		// slice, the full Rread body is already in our pooled buffer b.
		// Copy the count[4]+data[count] payload directly into dst[:count]
		// — bypassing newRMessage's Rread cache slot AND the Data alloc
		// inside proto.Rread.DecodeFrom. Two allocs eliminated per ReadAt.
		//
		// Body layout: type[1]+tag[2] = b[0:3] (parsed above), then
		// count[4] = b[3:7] and data[count] = b[7:7+count].
		if msgType == proto.TypeRread {
			if entry := c.inflight.lookup(tag); entry != nil && entry.dst != nil {
				if len(b) < 7 {
					bufpool.PutMsgBuf(bufPtr)
					c.logger.Warn("client: Rread body too short",
						slog.Int("len", len(b)),
					)
					c.signalShutdown()
					return
				}
				count := binary.LittleEndian.Uint32(b[3:7])
				// Match proto.Rread.DecodeFrom's MaxDataSize guard.
				if count > proto.MaxDataSize {
					bufpool.PutMsgBuf(bufPtr)
					c.logger.Warn("client: Rread count exceeds MaxDataSize",
						slog.Uint64("count", uint64(count)),
					)
					c.signalShutdown()
					return
				}
				// Pitfall 1 (24-RESEARCH.md): server returned more bytes
				// than the caller asked for. Spec says count <= request.count;
				// any violation is a protocol error — never silently truncate.
				if count > uint32(len(entry.dst)) {
					bufpool.PutMsgBuf(bufPtr)
					c.logger.Warn("client: Rread count > dst",
						slog.Uint64("count", uint64(count)),
						slog.Int("dst_len", len(entry.dst)),
					)
					c.signalShutdown()
					return
				}
				// Pitfall 1 variant: count claims more bytes than the
				// frame body actually carries (frame corruption). Fatal.
				if int(count) > len(b)-7 {
					bufpool.PutMsgBuf(bufPtr)
					c.logger.Warn("client: Rread count exceeds body",
						slog.Uint64("count", uint64(count)),
						slog.Int("body_remaining", len(b)-7),
					)
					c.signalShutdown()
					return
				}
				if count > 0 {
					copy(entry.dst[:count], b[7:7+count])
				}
				entry.n = int(count)
				bufpool.PutMsgBuf(bufPtr)
				// Sentinel-msg deliver — readAtZeroCopy identifies the
				// fast-path success via pointer equality `r == rreadSentinelOK`.
				c.inflight.deliver(tag, rreadSentinelOK)
				continue
			}
		}

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
	// Phase 21 (.L-only advanced ops): getattr/setattr/statfs, symlink/
	// readlink, locks, xattr two-phase, link/mknod/rename/renameat/
	// unlinkat. Dialect gating is defense-in-depth — per-op
	// requireDialect at the ops layer is the primary enforcement point.
	// A misbehaving peer that emits a .L-only R-type on a .u Conn falls
	// through to the default arm and triggers signalShutdown, the
	// correct behaviour for corrupted framing.
	//
	// None cached per client/msgcache.go — these are cold paths per the
	// server-side Phase 13-05 profile audit.
	case proto.TypeRgetattr:
		if c.dialect == protocolL {
			return &p9l.Rgetattr{}, nil
		}
	case proto.TypeRsetattr:
		if c.dialect == protocolL {
			return &p9l.Rsetattr{}, nil
		}
	case proto.TypeRstatfs:
		if c.dialect == protocolL {
			return &p9l.Rstatfs{}, nil
		}
	case proto.TypeRsymlink:
		if c.dialect == protocolL {
			return &p9l.Rsymlink{}, nil
		}
	case proto.TypeRreadlink:
		if c.dialect == protocolL {
			return &p9l.Rreadlink{}, nil
		}
	case proto.TypeRlock:
		if c.dialect == protocolL {
			return &p9l.Rlock{}, nil
		}
	case proto.TypeRgetlock:
		if c.dialect == protocolL {
			return &p9l.Rgetlock{}, nil
		}
	case proto.TypeRxattrwalk:
		if c.dialect == protocolL {
			return &p9l.Rxattrwalk{}, nil
		}
	case proto.TypeRxattrcreate:
		if c.dialect == protocolL {
			return &p9l.Rxattrcreate{}, nil
		}
	case proto.TypeRlink:
		if c.dialect == protocolL {
			return &p9l.Rlink{}, nil
		}
	case proto.TypeRmknod:
		if c.dialect == protocolL {
			return &p9l.Rmknod{}, nil
		}
	case proto.TypeRrename:
		if c.dialect == protocolL {
			return &p9l.Rrename{}, nil
		}
	case proto.TypeRrenameat:
		if c.dialect == protocolL {
			return &p9l.Rrenameat{}, nil
		}
	case proto.TypeRunlinkat:
		if c.dialect == protocolL {
			return &p9l.Runlinkat{}, nil
		}
	// Phase 21 (.u-only stat ops).
	case proto.TypeRstat:
		if c.dialect == protocolU {
			return &p9u.Rstat{}, nil
		}
	case proto.TypeRwstat:
		if c.dialect == protocolU {
			return &p9u.Rwstat{}, nil
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

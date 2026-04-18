package server

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
	"time"

	"github.com/dotwaffle/ninep/internal/bufpool"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
	"github.com/dotwaffle/ninep/proto/p9u"

	"context"
	"sync"
)

// protocol identifies the negotiated 9P dialect for a connection.
type protocol uint8

const (
	protocolNone protocol = iota
	protocolL             // 9P2000.L
	protocolU             // 9P2000.u
)

// String returns the version string for the protocol.
func (p protocol) String() string {
	switch p {
	case protocolL:
		return "9P2000.L"
	case protocolU:
		return "9P2000.u"
	default:
		return "unknown"
	}
}

// codec abstracts protocol-specific encode/decode operations.
type codec struct {
	encode func(w io.Writer, tag proto.Tag, msg proto.Message) error
	decode func(r io.Reader) (proto.Tag, proto.Message, error)
}

var (
	codecL = codec{encode: p9l.Encode, decode: p9l.Decode}
	codecU = codec{encode: p9u.Encode, decode: p9u.Decode}
)

// minMsize is the minimum acceptable negotiated msize. A message must fit at
// least a header plus a small error response.
const minMsize = 256

// negotiationResult carries the validated outcome of a Tversion exchange:
// the negotiated msize, the selected protocol, the codec, and the version
// string to echo back to the client. selected == protocolNone means the
// client requested an unsupported version; the caller still echoes
// result.version ("unknown") to the client but must NOT transition into a
// serving state.
type negotiationResult struct {
	msize    uint32
	selected protocol
	codec    codec
	version  string // "9P2000.L", "9P2000.u", or "unknown"
}

// negotiate validates a Tversion request against server limits and selects a
// protocol. Returns ErrMsizeTooSmall if the negotiated msize falls below
// minMsize. Pure logic -- no I/O, no connection state mutation, no locks.
// Callers apply the result to conn fields after handling their own pre/post
// steps (e.g. handleReVersion's drain+clunk choreography). See
// .planning/phases/10/10-CONTEXT.md D-SIMP-01.
func (c *conn) negotiate(tv *proto.Tversion) (negotiationResult, error) {
	msize := min(tv.Msize, c.server.maxMsize)
	if msize < minMsize {
		return negotiationResult{}, ErrMsizeTooSmall
	}
	res := negotiationResult{msize: msize, version: tv.Version}
	switch tv.Version {
	case "9P2000.L":
		res.selected = protocolL
		res.codec = codecL
	case "9P2000.u":
		res.selected = protocolU
		res.codec = codecU
	default:
		res.version = "unknown"
		// selected stays protocolNone; codec stays zero value.
	}
	return res, nil
}

// releaser is implemented by response messages that carry pooled buffers
// which must be returned to the pool after wire encoding completes. The
// dispatching goroutine in handleRequest hands the response to
// sendResponseInline, which calls Release after the writev completes.
// Currently used by pooledRread and pooledRreaddir in bridge.go.
type releaser interface {
	Release()
}

// conn represents a single client connection to the server.
type conn struct {
	server   *Server
	nc       net.Conn
	fids     *fidTable
	maxFids  int // Copied from server.maxFids; 0 = unlimited (per-connection cap).
	protocol protocol
	msize    uint32 // Negotiated msize (0 until version negotiation).
	codec    codec

	// writeMu serializes all writes to nc. Dispatching goroutines acquire
	// it in sendResponseInline, and writeRaw (used during version
	// negotiation) takes it as well. This prevents interleaved wire frames
	// (GO-CC-3).
	writeMu sync.Mutex

	// encHdr holds the 7-byte response header (size[4] + type[1] + tag[2])
	// between "fill" and "writev" inside sendResponseInline. Guarded by
	// writeMu; storing it on conn avoids per-response heap escape.
	encHdr [proto.HeaderSize]byte

	// encBufsArr is the backing array for the net.Buffers slice built in
	// sendResponseInline. Payloader responses use all three entries
	// (hdr, fixedBody, payload); non-Payloader responses use two.
	// Guarded by writeMu.
	encBufsArr [3][]byte

	// inflight tracks per-request goroutines for flush cancellation and
	// drain-on-disconnect.
	inflight *inflightMap

	// Recv-mutex worker model. A single goroutine type drives the receive
	// loop: lock recvMu, read one message, decide whether to spawn a
	// successor, unlock recvMu, dispatch, send response inline, loop. The
	// same goroutine that reads the bytes off the wire is the one that
	// handles the request and writes the reply -- no inter-goroutine
	// handoff.
	//
	// recvIdle counts goroutines parked in recvMu.Lock() waiting for their
	// turn to read. Incremented BEFORE Lock and decremented AFTER Lock; this
	// makes "recvIdle == 0" the precise predicate "no sibling is waiting to
	// take over the wire". When a goroutine releases recvMu and observes
	// recvIdle == 0 AND the worker count is below maxInflight, it spawns a
	// replacement.
	//
	// recvShutdown is set under recvMu by the first goroutine to observe a
	// recv error; siblings observe it on acquire and exit without reading.
	//
	// workerCount enforces the WithMaxInflight cap. recvWG tracks all
	// handleRequest goroutines for cleanup drain.
	// recvShutdownOnce/recvShutdownCh form a one-shot signal: the first
	// handleRequest goroutine to observe a recv error closes recvShutdownCh
	// so serve() can begin cleanup immediately. Without this, serve would
	// have to wait for recvWG to reach zero before initiating cleanup --
	// but handlers blocked in dispatch only return AFTER cleanup cancels
	// their contexts, which would deadlock.
	recvMu           sync.Mutex
	recvIdle         atomic.Int32
	recvShutdown     bool
	recvShutdownCh   chan struct{}
	recvShutdownOnce sync.Once
	recvWG           sync.WaitGroup
	workerCount      atomic.Int32

	logger *slog.Logger

	// handler is the middleware-wrapped dispatch chain. Built once in newConn
	// from chain(dispatch, server.middlewares). If no middleware is configured,
	// this is a direct call to dispatch with zero overhead.
	handler Handler

	// otelInst holds connection-level OTel gauge instruments. Nil when no
	// MeterProvider is configured (zero overhead).
	otelInst *connOTelInstruments
}

// newConn creates a new conn for the given server and network connection.
func newConn(s *Server, nc net.Conn) *conn {
	c := &conn{
		server:         s,
		nc:             nc,
		fids:           newFidTable(),
		maxFids:        s.maxFids,
		inflight:       newInflightMap(),
		recvShutdownCh: make(chan struct{}),
		logger:         s.logger.With(slog.String("remote", nc.RemoteAddr().String())),
	}
	// Build the middleware-wrapped dispatch chain. The closure captures c so
	// it must be created after c is initialized. If no middleware is
	// configured, chain returns the inner handler directly (zero overhead).
	inner := func(ctx context.Context, tag proto.Tag, msg proto.Message) proto.Message {
		return c.dispatch(ctx, tag, msg)
	}

	// If either probe at server.New detected a recording tracer or enabled
	// meter, prepend OTel middleware (outermost) and create connection-level
	// gauge instruments. The nil-to-noop fallback that previously lived here
	// has been moved to server.New (D-04): by the time we reach this block,
	// either (a) s.tracerRecording and s.meterEnabled are both false and we
	// skip the install entirely (short-circuit path -- no middleware call
	// frame, no context.WithValue wrap, no span.Start), or (b) at least one
	// is true and both s.tracerProvider and s.meterProvider are non-nil.
	mws := s.middlewares
	if s.tracerRecording || s.meterEnabled {
		mws = append([]Middleware{newOTelMiddleware(s.tracerProvider, s.meterProvider, c)}, mws...)
		c.otelInst = newConnOTelInstruments(s.meterProvider)
	}

	c.handler = chain(inner, mws)
	return c
}

// serve runs the connection lifecycle: version negotiation, then the
// recv-mutex worker loop. It blocks until the connection is closed or the
// context is cancelled.
func (c *conn) serve(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() { _ = c.nc.Close() }()

	if err := c.negotiateVersion(ctx); err != nil {
		c.logger.Debug("version negotiation failed", slog.Any("error", err))
		return
	}

	// Record connection start for OTel gauge.
	c.otelInst.recordConnChange(1)
	defer c.otelInst.recordConnChange(-1)

	// Inject connection metadata into context for node handlers.
	ctx = withConnInfo(ctx, &ConnInfo{
		Protocol:   c.protocol.String(),
		Msize:      c.msize,
		RemoteAddr: c.nc.RemoteAddr().String(),
	})

	// Close the net.Conn when context is cancelled to unblock reads (GO-CC-2).
	go func() {
		<-ctx.Done()
		_ = c.nc.Close()
	}()

	// Drive the recv-mutex worker model. Spawn the first handleRequest
	// goroutine; it lazy-spawns successors on demand under recvMu (bounded
	// by maxInflight) so the receive pipeline self-perpetuates.
	c.workerCount.Add(1)
	c.recvWG.Add(1)
	go c.handleRequest(ctx)

	// Wait for the first signal that the recv side has shut down. This
	// fires when EITHER (a) the goroutine holding recvMu observes a recv
	// error and signals shutdown, OR (b) the serve context is cancelled
	// and the watcher closes nc, which causes the recvMu-holder to error
	// out. We must NOT wait on recvWG here -- handlers blocked in
	// dispatch won't return until cleanup cancels their contexts, so
	// gating cleanup on recvWG would deadlock.
	select {
	case <-c.recvShutdownCh:
	case <-ctx.Done():
		// Ensure recvShutdownCh is closed so any concurrent observer
		// also sees the signal. signalRecvShutdown is idempotent.
		c.signalRecvShutdown()
	}

	// Orderly shutdown: cancel inflight, drain with deadline, close nc,
	// wait for any straggling handleRequest goroutines, then clunk fids.
	c.cleanup()
}

// signalRecvShutdown is the one-shot signal that the recv side has shut
// down. The first goroutine to observe a recv error (or the serve goroutine
// on ctx.Done) closes recvShutdownCh so cleanup can begin. Idempotent --
// safe to call from multiple goroutines.
func (c *conn) signalRecvShutdown() {
	c.recvShutdownOnce.Do(func() {
		close(c.recvShutdownCh)
	})
}

// negotiateVersion reads the first Tversion from the client and negotiates
// protocol version and msize. On success, c.protocol, c.msize, and c.codec
// are set.
func (c *conn) negotiateVersion(ctx context.Context) error {
	// Set read deadline for the initial negotiation if idle timeout is configured.
	if c.server.idleTimeout > 0 {
		if err := c.nc.SetReadDeadline(time.Now().Add(c.server.idleTimeout)); err != nil {
			return fmt.Errorf("set initial read deadline: %w", err)
		}
	}

	// Read the raw message header: size[4] + type[1] + tag[2].
	size, err := proto.ReadUint32(c.nc)
	if err != nil {
		return fmt.Errorf("read version size: %w", err)
	}
	if size < uint32(proto.HeaderSize) {
		return fmt.Errorf("version message too small: %d < %d", size, proto.HeaderSize)
	}

	msgType, err := proto.ReadUint8(c.nc)
	if err != nil {
		return fmt.Errorf("read version type: %w", err)
	}
	if proto.MessageType(msgType) != proto.TypeTversion {
		// First message must be Tversion; close connection.
		return ErrNotNegotiated
	}

	tag, err := proto.ReadUint16(c.nc)
	if err != nil {
		return fmt.Errorf("read version tag: %w", err)
	}

	// Decode Tversion body.
	bodySize := int64(size) - int64(proto.HeaderSize)
	var tver proto.Tversion
	if err := tver.DecodeFrom(io.LimitReader(c.nc, bodySize)); err != nil {
		return fmt.Errorf("decode tversion: %w", err)
	}

	// Validate msize + select protocol via shared helper (D-SIMP-01).
	res, err := c.negotiate(&tver)
	if err != nil {
		return err // ErrMsizeTooSmall
	}

	// Send Rversion response manually (codec not yet selected for the first response).
	rver := &proto.Rversion{Msize: res.msize, Version: res.version}
	if err := c.writeRaw(proto.Tag(tag), rver); err != nil {
		return fmt.Errorf("send rversion: %w", err)
	}

	if res.selected == protocolNone {
		return ErrNotNegotiated
	}

	c.msize = res.msize
	c.protocol = res.selected
	c.codec = res.codec

	c.logger.Debug("version negotiated",
		slog.String("version", res.version),
		slog.Uint64("msize", uint64(res.msize)),
	)
	return nil
}

// writeRaw encodes a single message directly to the connection, bypassing
// sendResponseInline. Used during version negotiation (both initial and
// mid-connection re-negotiation) where the codec may not yet be selected.
// Acquires writeMu to serialize writes against dispatching goroutines and
// the raw negotiation path.
func (c *conn) writeRaw(tag proto.Tag, msg proto.Message) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	// Set write deadline if idle timeout is configured.
	if c.server.idleTimeout > 0 {
		if err := c.nc.SetWriteDeadline(time.Now().Add(c.server.idleTimeout)); err != nil {
			return fmt.Errorf("set write deadline: %w", err)
		}
	}

	// Body buffer is borrowed from the shared pool and returned via defer.
	// Passing the pooled *bytes.Buffer into msg.EncodeTo triggers the
	// proto.Write* zero-alloc fast path (plan 08-02). PutBuf runs AFTER
	// c.nc.Write returns; net.Conn.Write copies its input synchronously,
	// so the buffer is no longer referenced when it returns to the pool.
	body := bufpool.GetBuf()
	defer bufpool.PutBuf(body)
	if err := msg.EncodeTo(body); err != nil {
		return fmt.Errorf("encode %s body: %w", msg.Type(), err)
	}

	size := uint32(proto.HeaderSize) + uint32(body.Len())
	if err := proto.WriteUint32(c.nc, size); err != nil {
		return fmt.Errorf("write size: %w", err)
	}
	if err := proto.WriteUint8(c.nc, uint8(msg.Type())); err != nil {
		return fmt.Errorf("write type: %w", err)
	}
	if err := proto.WriteUint16(c.nc, uint16(tag)); err != nil {
		return fmt.Errorf("write tag: %w", err)
	}
	if _, err := c.nc.Write(body.Bytes()); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	return nil
}

// handleRequest is one driver of the recv-mutex worker model. The loop:
//
//  1. Acquires recvMu (bumping recvIdle for the spawn-replacement predicate).
//  2. Reads one message: 4-byte size header, then body.
//  3. Decodes the body INSIDE recvMu (so per-iteration scratch buffers stay
//     safely owned by the lock holder).
//  4. Decides whether to spawn a replacement (skip on Tversion to keep this
//     goroutine the sole reader during re-negotiation).
//  5. Releases recvMu.
//  6. Handles errors / Tversion / Tflush / dispatch outside the lock.
//  7. Loops.
//
// The same goroutine that reads the bytes is the one that handles the
// request and writes the response via dispatchInline -> sendResponseInline.
// Per-iteration locals (hdrBuf, bodyReader) sit on this goroutine's stack so
// concurrent siblings cannot corrupt them.
func (c *conn) handleRequest(ctx context.Context) {
	defer c.recvWG.Done()
	defer c.workerCount.Add(-1)

	for {
		// Per-iteration scratch. Must be locals (not on conn) because
		// multiple goroutines now run this loop.
		var hdrBuf [4]byte
		var bodyReader bytes.Reader

		// recvIdle++ BEFORE Lock; recvIdle-- AFTER Lock. This makes
		// "recvIdle == 0" the precise predicate "no sibling is parked
		// waiting to take over the wire" (RESEARCH §1, p9 verbatim).
		c.recvIdle.Add(1)
		c.recvMu.Lock()
		c.recvIdle.Add(-1)

		if c.recvShutdown {
			// A sibling already saw a recv error; exit without reading.
			c.recvMu.Unlock()
			return
		}

		// Per-iteration read deadline for idle timeout. Inside recvMu so
		// only one goroutine ever touches the read deadline at a time.
		if c.server.idleTimeout > 0 {
			if err := c.nc.SetReadDeadline(time.Now().Add(c.server.idleTimeout)); err != nil {
				c.logger.Warn("failed to set read deadline", slog.Any("error", err))
				c.recvShutdown = true
				c.recvMu.Unlock()
				c.signalRecvShutdown()
				return
			}
		}

		// Read 4-byte size header.
		if _, err := io.ReadFull(c.nc, hdrBuf[:]); err != nil {
			if !isExpectedCloseError(err) {
				c.logger.Debug("read error", slog.Any("error", err))
			}
			c.recvShutdown = true
			c.recvMu.Unlock()
			c.signalRecvShutdown()
			return
		}
		size := binary.LittleEndian.Uint32(hdrBuf[:])
		if size < uint32(proto.HeaderSize) {
			c.logger.Warn("message too small", slog.Uint64("size", uint64(size)))
			c.recvShutdown = true
			c.recvMu.Unlock()
			c.signalRecvShutdown()
			return
		}
		if size > c.msize {
			c.logger.Warn("message exceeds msize",
				slog.Uint64("size", uint64(size)),
				slog.Uint64("msize", uint64(c.msize)),
			)
			c.recvShutdown = true
			c.recvMu.Unlock()
			c.signalRecvShutdown()
			return
		}

		// Read body: type[1] + tag[2] + payload.
		bufPtr := bufpool.GetMsgBuf(int(size - 4))
		b := (*bufPtr)[:size-4]
		if _, err := io.ReadFull(c.nc, b); err != nil {
			bufpool.PutMsgBuf(bufPtr)
			if !isExpectedCloseError(err) {
				c.logger.Debug("read body error", slog.Any("error", err))
			}
			c.recvShutdown = true
			c.recvMu.Unlock()
			c.signalRecvShutdown()
			return
		}

		// Parse header.
		msgType := proto.MessageType(b[0])
		tag := proto.Tag(binary.LittleEndian.Uint16(b[1:3]))

		// Spawn-replacement decision: only if a sibling is NOT already
		// parked on recvMu AND we are below the maxInflight cap. Skip on
		// Tversion -- handleReVersion drains all inflight and mutates
		// c.msize/c.protocol/c.codec; a sibling reading with the old
		// codec mid-renegotiation would corrupt the stream (RESEARCH P4).
		spawnReplacement := false
		if msgType != proto.TypeTversion &&
			c.recvIdle.Load() == 0 &&
			c.workerCount.Load() < int32(c.server.maxInflight) {
			spawnReplacement = true
			c.workerCount.Add(1)
			c.recvWG.Add(1)
		}

		// Decode INSIDE recvMu. Branches are mutually exclusive
		// (if/else if/else): unknownType skips decode entirely; Twrite
		// defers buf release (Data aliases buf); other types copy via
		// DecodeFrom and release the buf immediately.
		var msg proto.Message
		var deferredBufPtr *[]byte
		var decodeErr error
		var unknownType bool

		msg, newMsgErr := c.newMessage(msgType)
		if newMsgErr != nil {
			// Unknown message type. Do NOT touch msg (it is nil).
			// Release the buf here; do NOT enter any decode branch.
			unknownType = true
			bufpool.PutMsgBuf(bufPtr)
		} else if tw, ok := msg.(*proto.Twrite); ok {
			if err := tw.DecodeFromBuf(b[3:]); err != nil {
				decodeErr = err
				// Twrite decode failed before aliasing took effect:
				// release buf now; the cached msg is returned in the
				// decodeErr branch outside the lock.
				bufpool.PutMsgBuf(bufPtr)
			} else {
				// Successful Twrite: m.Data aliases bufPtr; defer
				// release to dispatchInline.
				deferredBufPtr = bufPtr
			}
		} else {
			bodyReader.Reset(b[3:])
			if err := msg.DecodeFrom(&bodyReader); err != nil {
				decodeErr = err
			}
			// DecodeFrom copied; safe to release immediately
			// (regardless of decodeErr).
			bufpool.PutMsgBuf(bufPtr)
		}

		if spawnReplacement {
			go c.handleRequest(ctx)
		}
		c.recvMu.Unlock()

		// Outside recvMu from here on.
		if unknownType {
			// msg is nil; nothing to release to msgcache.
			c.sendError(tag, proto.ENOSYS)
			continue
		}
		if decodeErr != nil {
			c.logger.Warn("decode error",
				slog.String("type", msgType.String()),
				slog.Any("error", decodeErr),
			)
			// Decode failures on the wire are fatal for this conn -- we
			// cannot trust subsequent framing. Mirror the old behaviour:
			// return after marking the connection shut down.
			//
			// msg is non-nil (newMsgErr was nil) and was NOT dispatched,
			// so we own it; return it to the cache before exiting.
			putCachedMsg(msg)

			// Set recvShutdown so siblings exit cleanly on next iter.
			c.recvMu.Lock()
			c.recvShutdown = true
			c.recvMu.Unlock()
			c.signalRecvShutdown()

			// Closing nc fast-paths any sibling already inside a Read
			// syscall out of it. recvShutdown alone only catches siblings
			// at next Lock acquire. net.Conn.Close is idempotent (returns
			// ErrClosed which we ignore); the redundant close is
			// intentional belt-and-braces.
			_ = c.nc.Close()
			return
		}

		// Tversion mid-conn: handle inline (we deliberately did NOT
		// spawn a replacement above; we are the sole reader during
		// re-negotiation). After handleReVersion returns, the loop
		// continues -- the next iteration will spawn a replacement
		// normally.
		if msgType == proto.TypeTversion {
			c.handleReVersion(ctx, tag, b[3:])
			putCachedMsg(msg)
			continue
		}

		// Tflush short-circuit. dispatch.go has no case *proto.Tflush;
		// routing Tflush through dispatch would return ENOSYS. Tflush
		// also operates on OTHER tags' inflight state and must NOT
		// itself create an inflight entry, so we cannot call
		// inflight.start or dispatchInline for it. Mirror the old
		// short-circuit explicitly here, AFTER recvMu unlock so a
		// sibling can already be reading the next message.
		if tf, ok := msg.(*proto.Tflush); ok {
			resp := c.handleFlush(ctx, tf)
			// sendResponseInline accepts a nil releaser; Rflush has no
			// pooled buffers to release.
			c.sendResponseInline(tag, resp, nil)
			putCachedMsg(msg)
			// No deferredBufPtr possible here (Tflush is not Twrite),
			// but defensively release if non-nil.
			if deferredBufPtr != nil {
				bufpool.PutMsgBuf(deferredBufPtr)
			}
			continue
		}

		// Per-request context with lazy-cancel flush support. Pooled via
		// requestCtxPool; returned to the pool in dispatchInline's defer
		// chain AFTER inflight.finish (LIFO ordering — putRequestCtx is
		// registered first so it executes last). See D-07 / Pitfall 4.
		rctx := getRequestCtx(ctx)
		c.inflight.start(tag, rctx)

		// Dispatch + send response inline (this folds in the work that
		// was previously the worker's responsibility).
		c.dispatchInline(rctx, tag, msg, deferredBufPtr)
	}
}

// dispatchInline runs one request through the middleware + dispatch chain
// with panic recovery, sends the response, and releases pooled buffers,
// cached message structs, and inflight tag tracking. Called from
// handleRequest after recvMu is released.
//
// bufPtr, when non-nil, points at a pooled message-body buffer that the
// request aliases (currently only Twrite.Data). It MUST be returned to
// the pool BEFORE putCachedMsg: defer is LIFO and Twrite.Data aliases the
// buffer; clearing the cache before release would zero Data while it
// still references the recycled buffer (RESEARCH P10).
func (c *conn) dispatchInline(rctx *requestCtx, tag proto.Tag, msg proto.Message, bufPtr *[]byte) {
	// LIFO: registered FIRST so it runs LAST — after c.inflight.finish(tag)
	// in the defer below. Required by D-07 / Pitfall 4: a concurrent Tflush
	// must be able to look up `tag` in the inflight map until finish()
	// removes it; only then is it safe to recycle rctx back to the pool.
	// Violating this ordering causes Tflush to call flush() on a
	// pool-recycled requestCtx belonging to an unrelated later request.
	defer putRequestCtx(rctx)

	defer func() {
		if bufPtr != nil {
			bufpool.PutMsgBuf(bufPtr)
		}
		// MUST run after PutMsgBuf (defer is LIFO; source order matters).
		putCachedMsg(msg)
		if r := recover(); r != nil {
			// SERV-06: Handler panic -> EIO, never crash the server.
			c.logger.Error("handler panic",
				slog.Any("panic", r),
				slog.String("message_type", msg.Type().String()),
			)
			c.sendResponse(tag, c.errorMsg(proto.EIO))
		}
		c.inflight.finish(tag)
	}()

	resp := c.handler(rctx, tag, msg) // *requestCtx satisfies context.Context
	if resp != nil {
		// Store the releaser interface verbatim -- taking r.Release as
		// a method value would allocate a heap closure on every request.
		// Passing the interface value costs no extra alloc and
		// sendResponseInline invokes release.Release() virtually.
		var release releaser
		if r, ok := resp.(releaser); ok {
			release = r
		}
		c.sendResponseInline(tag, resp, release)
	}
}

// handleReVersion handles a Tversion message received mid-connection. Per the
// 9P spec, Tversion aborts all outstanding I/O and clunks all fids, then
// re-negotiates the protocol version and msize.
func (c *conn) handleReVersion(_ context.Context, tag proto.Tag, body []byte) {
	// Cancel all inflight request contexts first (WR-01), then wait for
	// handlers to drain with a deadline before mutating connection state
	// (CR-02). This ensures no handler goroutine reads c.msize, c.protocol,
	// or c.codec while we are updating them (GO-CC-3).
	c.inflight.cancelAll()
	drainCtx, drainCancel := context.WithTimeout(context.Background(), cleanupDeadline)
	defer drainCancel()
	if err := c.inflight.waitWithDeadline(drainCtx); err != nil {
		c.logger.Warn("re-negotiation: timed out waiting for inflight drain",
			slog.Int("remaining", c.inflight.len()),
		)
	}

	// Clunk all fids and release handles/closers (matching cleanup pattern).
	states := c.fids.clunkAll()
	if len(states) > 0 {
		c.otelInst.recordFidChange(-int64(len(states)))
	}
	for _, fs := range states {
		releaseHandle(context.Background(), fs, c.logger)
		if closer, ok := fs.node.(NodeCloser); ok {
			if err := closer.Close(context.Background()); err != nil {
				c.logger.Debug("node close error during re-negotiation", slog.Any("error", err))
			}
		}
	}

	var tver proto.Tversion
	if err := tver.DecodeFrom(bytes.NewReader(body)); err != nil {
		c.logger.Warn("re-negotiation decode error", slog.Any("error", err))
		return
	}

	// Validate msize + select protocol via shared helper (D-SIMP-01).
	res, err := c.negotiate(&tver)
	if err != nil {
		c.logger.Warn("re-negotiation msize too small", slog.Uint64("msize", uint64(tver.Msize)))
		return
	}

	// Send Rversion directly via writeRaw, which acquires writeMu to
	// prevent interleaving with other dispatchers' writes (CR-01).
	rver := &proto.Rversion{Msize: res.msize, Version: res.version}
	if err := c.writeRaw(tag, rver); err != nil {
		c.logger.Warn("re-negotiation send error", slog.Any("error", err))
		return
	}

	if res.selected == protocolNone {
		return
	}

	c.msize = res.msize
	c.protocol = res.selected
	c.codec = res.codec
}

// newMessage returns a zero-value message struct for the given type based on
// the negotiated protocol. For Phase 2, only lifecycle messages are handled;
// unknown types return an error.
func (c *conn) newMessage(t proto.MessageType) (proto.Message, error) {
	switch t {
	// Shared base message types handled in all protocols.
	case proto.TypeTattach:
		return &proto.Tattach{}, nil
	case proto.TypeTwalk:
		return twalkCache.Get(), nil
	case proto.TypeTclunk:
		return tclunkCache.Get(), nil
	case proto.TypeTflush:
		return &proto.Tflush{}, nil
	case proto.TypeTauth:
		return &proto.Tauth{}, nil
	case proto.TypeTread:
		return treadCache.Get(), nil
	case proto.TypeTwrite:
		return twriteCache.Get(), nil
	case proto.TypeTremove:
		return &proto.Tremove{}, nil

	// 9P2000.L-specific message types for capability bridge.
	case proto.TypeTlopen:
		return tlopenCache.Get(), nil
	case proto.TypeTlcreate:
		return tlcreateCache.Get(), nil
	case proto.TypeTgetattr:
		return tgetattrCache.Get(), nil
	case proto.TypeTsetattr:
		return &p9l.Tsetattr{}, nil
	case proto.TypeTreaddir:
		return &p9l.Treaddir{}, nil
	case proto.TypeTmkdir:
		return &p9l.Tmkdir{}, nil
	case proto.TypeTsymlink:
		return &p9l.Tsymlink{}, nil
	case proto.TypeTlink:
		return &p9l.Tlink{}, nil
	case proto.TypeTmknod:
		return &p9l.Tmknod{}, nil
	case proto.TypeTreadlink:
		return &p9l.Treadlink{}, nil
	case proto.TypeTstatfs:
		return &p9l.Tstatfs{}, nil
	case proto.TypeTfsync:
		return &p9l.Tfsync{}, nil
	case proto.TypeTunlinkat:
		return &p9l.Tunlinkat{}, nil
	case proto.TypeTrenameat:
		return &p9l.Trenameat{}, nil
	case proto.TypeTrename:
		return &p9l.Trename{}, nil
	case proto.TypeTlock:
		return &p9l.Tlock{}, nil
	case proto.TypeTgetlock:
		return &p9l.Tgetlock{}, nil
	case proto.TypeTxattrwalk:
		return &p9l.Txattrwalk{}, nil
	case proto.TypeTxattrcreate:
		return &p9l.Txattrcreate{}, nil
	default:
		return nil, fmt.Errorf("unknown message type %d", t)
	}
}

// sendResponseInline encodes a response and writes it to the connection
// directly from the dispatching goroutine. There is no inter-goroutine
// handoff between the goroutine that handled the request and the wire
// write -- the same goroutine encodes, takes writeMu, and issues the writev.
//
// Serialises concurrent writes via writeMu, and uses the conn-resident
// encHdr/encBufsArr buffers (guarded by writeMu) to avoid per-response
// allocation. On TCP / unix-domain sockets the write is a single writev
// syscall covering header + body (+ optional Payloader payload).
//
// rel, when non-nil, has its Release method called after the writev
// completes so pooled Rread/Rreaddir buffers return to their pool even
// when the write fails.
func (c *conn) sendResponseInline(tag proto.Tag, msg proto.Message, rel releaser) {
	// Encode outside writeMu to keep the critical section short.
	body := bufpool.GetBuf()

	var payload []byte
	if pl, ok := msg.(proto.Payloader); ok {
		if err := pl.EncodeFixed(body); err != nil {
			c.logger.Warn("encode error",
				slog.String("type", msg.Type().String()),
				slog.Any("error", err),
			)
			bufpool.PutBuf(body)
			if rel != nil {
				rel.Release()
			}
			return
		}
		payload = pl.Payload()
	} else if err := msg.EncodeTo(body); err != nil {
		c.logger.Warn("encode error",
			slog.String("type", msg.Type().String()),
			slog.Any("error", err),
		)
		bufpool.PutBuf(body)
		if rel != nil {
			rel.Release()
		}
		return
	}

	c.writeMu.Lock()
	if c.server.idleTimeout > 0 {
		if err := c.nc.SetWriteDeadline(time.Now().Add(c.server.idleTimeout)); err != nil {
			c.writeMu.Unlock()
			c.logger.Warn("failed to set write deadline", slog.Any("error", err))
			bufpool.PutBuf(body)
			if rel != nil {
				rel.Release()
			}
			return
		}
	}

	size := uint32(proto.HeaderSize) + uint32(body.Len()) + uint32(len(payload))
	binary.LittleEndian.PutUint32(c.encHdr[0:4], size)
	c.encHdr[4] = uint8(msg.Type())
	binary.LittleEndian.PutUint16(c.encHdr[5:7], uint16(tag))

	c.encBufsArr[0] = c.encHdr[:]
	c.encBufsArr[1] = body.Bytes()
	n := 2
	if len(payload) > 0 {
		c.encBufsArr[2] = payload
		n = 3
	}
	bufs := net.Buffers(c.encBufsArr[:n])
	_, err := bufs.WriteTo(c.nc)
	c.writeMu.Unlock()

	bufpool.PutBuf(body)
	if rel != nil {
		rel.Release()
	}

	if err != nil {
		c.logger.Warn("write error",
			slog.String("type", msg.Type().String()),
			slog.Any("error", err),
		)
	}
}

// sendResponse sends a response with no attached releaser. Thin wrapper
// over sendResponseInline for callers that don't have a pooled buffer
// to return (e.g. the panic-recovery error path).
func (c *conn) sendResponse(tag proto.Tag, msg proto.Message) {
	c.sendResponseInline(tag, msg, nil)
}

// sendError queues a protocol-appropriate error response.
func (c *conn) sendError(tag proto.Tag, errno proto.Errno) {
	c.sendResponse(tag, c.errorMsg(errno))
}

// errorMsg returns the protocol-appropriate error message.
func (c *conn) errorMsg(errno proto.Errno) proto.Message {
	switch c.protocol {
	case protocolU:
		return &p9u.Rerror{Ename: errno.Error(), Errno: errno}
	default:
		return &p9l.Rlerror{Ecode: errno}
	}
}

// isExpectedCloseError returns true for errors that indicate a normal
// connection shutdown (EOF, closed pipe, timeout).
func isExpectedCloseError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	if netErr, ok := errors.AsType[net.Error](err); ok && netErr.Timeout() {
		return true
	}
	return false
}

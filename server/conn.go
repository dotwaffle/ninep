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
	"github.com/dotwaffle/ninep/internal/wire"
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

	// negotiationRateLimit is the minimum time between Tversion requests
	// from the same connection.
	negotiationRateLimit = 100 * time.Millisecond

	// defaultHandshakeTimeout bounds the initial version handshake when no
	// idle timeout is configured, so a peer that connects and then stalls
	// cannot pin a goroutine, fd, and buffer indefinitely (slow-loris). It
	// applies only to the handshake; established connections without an idle
	// timeout remain deadline-free.
	defaultHandshakeTimeout = 30 * time.Second
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

// minMsize is the minimum acceptable negotiated msize. A message must fit at
// least a header plus a small error response.
const minMsize = 256

// negotiationResult carries the validated outcome of a Tversion exchange:
// the negotiated msize, the selected protocol, and the version string to
// echo back to the client. selected == protocolNone means the client
// requested an unsupported version; the caller still echoes
// result.version ("unknown") to the client but must NOT transition into a
// serving state.
type negotiationResult struct {
	msize    uint32
	selected protocol
	version  string // "9P2000.L", "9P2000.u", or "unknown"
}

// negotiate validates a Tversion request against server limits and selects a
// protocol. Returns ErrMsizeTooSmall if the negotiated msize falls below
// minMsize. Pure logic -- no I/O, no connection state mutation, no locks.
// Callers apply the result to conn fields after handling their own
// pre/post steps (e.g. handleReVersion's drain+clunk choreography).
func (c *conn) negotiate(tv *proto.Tversion) (negotiationResult, error) {
	msize := min(tv.Msize, c.server.maxMsize)
	if msize < minMsize {
		return negotiationResult{}, ErrMsizeTooSmall
	}
	res := negotiationResult{msize: msize, version: tv.Version}
	switch tv.Version {
	case "9P2000.L":
		res.selected = protocolL
	case "9P2000.u":
		res.selected = protocolU
	default:
		res.version = "unknown"
		// selected stays protocolNone.
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

	// lastNegotiation tracks the UnixNano time of the last Tversion request
	// for rate-limiting.
	lastNegotiation atomic.Int64

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
	// has been moved to server.New: by the time we reach this block,
	// either (a) s.tracerRecording and s.meterEnabled are both false and we
	// skip the install entirely (short-circuit path -- no middleware call
	// frame, no context.WithValue wrap, no span.Start), or (b) at least one
	// is true and both s.tracerProvider and s.meterProvider are non-nil.
	mws := s.middlewares
	if s.tracerRecording || s.meterEnabled {
		mws = append([]Middleware{s.otelCore.middleware(c)}, mws...)
		c.otelInst = s.connInst
	}
	if s.requestLogging {
		// Append (innermost) so the log fires inside any OTel span and the
		// per-conn logger, which is already trace-wrapped and carries the
		// remote address, emits trace_id/span_id with each request. Cap the
		// slice to its length so the append allocates a fresh array instead
		// of writing into s.middlewares' shared backing store.
		mws = append(mws[:len(mws):len(mws)], NewLoggingMiddleware(c.logger))
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
// protocol version and msize. On success, c.protocol and c.msize are set.
func (c *conn) negotiateVersion(ctx context.Context) error {
	// Bound the whole handshake (reading Tversion and writing Rversion) with a
	// deadline even when no idle timeout is configured, so a peer that opens a
	// connection and then stalls cannot pin a goroutine, fd, and buffer
	// forever. Established connections keep their idleTimeout-governed
	// behavior; the deadline is cleared below on success when no idle timeout
	// applies. writeRaw re-arms its own write deadline when idleTimeout > 0.
	handshakeTimeout := c.server.idleTimeout
	if handshakeTimeout <= 0 {
		handshakeTimeout = c.server.handshakeTimeout
	}
	if handshakeTimeout > 0 {
		if err := c.nc.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
			return fmt.Errorf("set handshake deadline: %w", err)
		}
	}

	// Read the raw message header: size[4] + type[1] + tag[2].
	// wire.ReadSize also validates size >= proto.HeaderSize internally,
	// keeping this cold path consistent with handleRequest's hot-path
	// framing (conn.go:453).
	size, err := wire.ReadSize(c.nc)
	if err != nil {
		return fmt.Errorf("read version size: %w", err)
	}
	// msize is not negotiated yet, so bound the frame against the largest
	// msize this server will ever accept; this keeps the body allocation
	// below safe against an oversized declared size.
	if size > c.server.maxMsize {
		return fmt.Errorf("version frame size %d exceeds maximum %d", size, c.server.maxMsize)
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

	// Read the full declared body before decoding, so any bytes the decoder
	// does not consume are still drained from the stream. Decoding straight
	// from an io.LimitReader would leave surplus bytes that the next ReadSize
	// would misparse as a frame prefix. Mirrors handleReVersion, which decodes
	// from a fully-read body buffer.
	body := make([]byte, int(size-proto.HeaderSize))
	if _, err := io.ReadFull(c.nc, body); err != nil {
		return fmt.Errorf("read tversion body: %w", err)
	}
	var tver proto.Tversion
	if err := tver.DecodeFrom(bytes.NewReader(body)); err != nil {
		return fmt.Errorf("decode tversion: %w", err)
	}

	// Validate msize + select protocol via shared helper (shared helper).
	res, err := c.negotiate(&tver)
	if err != nil {
		return err // ErrMsizeTooSmall
	}

	// Send Rversion response manually (protocol not yet selected for the first response).
	rver := &proto.Rversion{Msize: res.msize, Version: res.version}
	if err := c.writeRaw(proto.Tag(tag), rver); err != nil {
		return fmt.Errorf("send rversion: %w", err)
	}
	c.lastNegotiation.Store(time.Now().UnixNano())

	if res.selected == protocolNone {
		return ErrNotNegotiated
	}

	// Without an idle timeout the established connection is deadline-free, so
	// clear the handshake deadline; otherwise it would expire mid-session. The
	// hot read loop re-arms the deadline itself when idleTimeout > 0.
	if c.server.idleTimeout <= 0 {
		if err := c.nc.SetDeadline(time.Time{}); err != nil {
			return fmt.Errorf("clear handshake deadline: %w", err)
		}
	}

	c.msize = res.msize
	c.protocol = res.selected

	c.logger.Debug("version negotiated",
		slog.String("version", res.version),
		slog.Uint64("msize", uint64(res.msize)),
	)
	return nil
}

// writeRaw encodes a single message directly to the connection, bypassing
// sendResponseInline. Used during version negotiation (both initial and
// mid-connection re-negotiation) where the protocol may not yet be selected.
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
//  5. Releases recvMu -- except for Tversion, which runs handleReVersion
//     and releases recvMu only once c.msize/c.protocol are settled, so no
//     already-parked sibling can read a frame against stale values.
//  6. Handles errors / Tflush / dispatch outside the lock.
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
		// recvIdle++ BEFORE Lock; recvIdle-- AFTER Lock. This makes
		// "recvIdle == 0" the precise predicate "no sibling is parked
		// waiting to take over the wire" (p9 verbatim).
		c.recvIdle.Add(1)
		c.recvMu.Lock()
		c.recvIdle.Add(-1)

		if c.recvShutdown {
			// A sibling already saw a recv error; exit without reading.
			c.recvMu.Unlock()
			return
		}

		// readFrameLocked returns with recvMu still held on success; on
		// failure it has already shut the receive path down and released
		// the lock.
		f, ok := c.readFrameLocked()
		if !ok {
			return
		}

		if f.spawnReplacement {
			go c.handleRequest(ctx)
		}

		if f.msgType == proto.TypeTversion {
			// Keep recvMu held across the full re-negotiation
			// choreography. Releasing it here (as a plain request would)
			// lets an already-parked sibling acquire recvMu and read the
			// next frame against c.msize/c.protocol while handleReVersion
			// is still draining inflight and about to mutate those same
			// fields below -- a data race between the sibling's read and
			// this mutation.
			c.handleReVersion(ctx, f.tag, f.body)
			c.recvMu.Unlock()
			bufpool.PutMsgBuf(f.bufPtr)
			putCachedMsg(f.msg)
			continue
		}
		c.recvMu.Unlock()

		// Outside recvMu from here on.
		if f.unknownType {
			// msg is nil; nothing to release to msgcache.
			c.sendError(f.tag, proto.ENOSYS)
			continue
		}
		if f.decodeErr != nil {
			c.logger.Warn("decode error",
				slog.String("type", f.msgType.String()),
				slog.Any("error", f.decodeErr),
			)
			// Decode failures on the wire are fatal for this conn -- we
			// cannot trust subsequent framing.
			//
			// msg is non-nil (newMessage succeeded) and was NOT dispatched,
			// so we own it; return it to the cache before exiting.
			putCachedMsg(f.msg)
			c.shutdownRecv()
			return
		}

		// Tflush short-circuit. dispatch.go has no case *proto.Tflush;
		// routing Tflush through dispatch would return ENOSYS. Tflush
		// also operates on OTHER tags' inflight state and must NOT
		// itself create an inflight entry, so we cannot call
		// inflight.start or dispatchInline for it. Mirror the old
		// short-circuit explicitly here, AFTER recvMu unlock so a
		// sibling can already be reading the next message.
		if tf, ok := f.msg.(*proto.Tflush); ok {
			// handleFlush blocks until the flushed request's response is
			// written, then returns Rflush. It returns nil when the
			// connection is closing or it had to close the connection after
			// a drain timeout; in that case no Rflush is sent.
			if resp := c.handleFlush(ctx, tf); resp != nil {
				// sendResponseInline accepts a nil releaser; Rflush has no
				// pooled buffers to release.
				c.sendResponseInline(f.tag, resp, nil)
			}
			putCachedMsg(f.msg)
			// No deferredBufPtr possible here (Tflush is not Twrite),
			// but defensively release if non-nil.
			if f.deferredBufPtr != nil {
				bufpool.PutMsgBuf(f.deferredBufPtr)
			}
			continue
		}

		// Per-request context with lazy-cancel flush support. Pooled via
		// requestCtxPool; returned to the pool in dispatchInline's defer
		// chain AFTER inflight.finish (LIFO ordering - putRequestCtx is
		// registered first so it executes last).
		rctx := getRequestCtx(ctx)
		// Record the wire frame size so the OTel middleware can report
		// request size without re-encoding the decoded message.
		rctx.wireSize = f.size
		if !c.inflight.start(f.tag, rctx) {
			putRequestCtx(rctx)
			if f.deferredBufPtr != nil {
				bufpool.PutMsgBuf(f.deferredBufPtr)
			}
			putCachedMsg(f.msg)

			c.logger.Warn("duplicate in-flight tag",
				slog.Uint64("tag", uint64(f.tag)),
			)
			c.shutdownRecv()
			return
		}

		// Dispatch + send response inline (this folds in the work that
		// was previously the worker's responsibility).
		c.dispatchInline(rctx, f.tag, f.msg, f.deferredBufPtr)
	}
}

// recvFrame is one decoded request as produced by readFrameLocked. Exactly
// one of three shapes comes back on success:
//   - unknownType: msg is nil, no buffers held.
//   - Tversion (msgType == proto.TypeTversion): body aliases bufPtr, which
//     the caller must release after handleReVersion; msg is undecoded.
//   - anything else: msg is decoded (or decodeErr is set); deferredBufPtr
//     is non-nil only for Twrite, whose Data aliases it.
type recvFrame struct {
	size             uint32
	msgType          proto.MessageType
	tag              proto.Tag
	msg              proto.Message
	deferredBufPtr   *[]byte
	bufPtr           *[]byte
	body             []byte
	decodeErr        error
	unknownType      bool
	spawnReplacement bool
}

// readFrameLocked reads and decodes one frame from the wire. The caller
// must hold recvMu; on success the lock is STILL HELD when it returns (the
// caller decides the unlock point -- Tversion re-negotiation must keep it
// across handleReVersion). On failure it shuts the receive path down,
// releases the lock, and returns ok=false.
//
// It also makes the spawn-replacement decision (recvFrame.spawnReplacement)
// while the sibling-idle count is stable under the lock; the caller spawns
// after decode so the new sibling never observes a half-read frame.
func (c *conn) readFrameLocked() (recvFrame, bool) {
	var f recvFrame

	// Per-iteration read deadline for idle timeout. Inside recvMu so
	// only one goroutine ever touches the read deadline at a time.
	if c.server.idleTimeout > 0 {
		if err := c.nc.SetReadDeadline(time.Now().Add(c.server.idleTimeout)); err != nil {
			c.logger.Warn("failed to set read deadline", slog.Any("error", err))
			c.shutdownRecvLocked()
			return f, false
		}
	}

	// Read 4-byte size header. wire.ReadSize returns a descriptive
	// error when size < proto.HeaderSize; the shutdown path is
	// identical to any other read error.
	size, err := wire.ReadSize(c.nc)
	if err != nil {
		if !isExpectedCloseError(err) {
			c.logger.Debug("read error", slog.Any("error", err))
		}
		c.shutdownRecvLocked()
		return f, false
	}
	// msize validation is server policy and lives HERE -- between the
	// size-prefix read and the body allocation -- so a 4 GiB attacker
	// size never causes a body buffer to be requested. Do not move
	// this into internal/wire.
	if size > c.msize {
		c.logger.Warn("message exceeds msize",
			slog.Uint64("size", uint64(size)),
			slog.Uint64("msize", uint64(c.msize)),
		)
		c.shutdownRecvLocked()
		return f, false
	}
	f.size = size

	// Read body: type[1] + tag[2] + payload. bufpool.GetMsgBuf
	// returns a bucket-sized slice; we slice to the exact body length
	// so PutMsgBuf's bucket-cap match succeeds on release. wire.ReadBody
	// fills exactly len(b) bytes and MUST NOT resize b.
	bufPtr := bufpool.GetMsgBuf(int(size - 4))
	b := (*bufPtr)[:size-4]
	if err := wire.ReadBody(c.nc, b); err != nil {
		bufpool.PutMsgBuf(bufPtr)
		if !isExpectedCloseError(err) {
			c.logger.Debug("read body error", slog.Any("error", err))
		}
		c.shutdownRecvLocked()
		return f, false
	}

	// Parse header.
	f.msgType = proto.MessageType(b[0])
	f.tag = proto.Tag(binary.LittleEndian.Uint16(b[1:3]))

	// Spawn-replacement decision: only if a sibling is NOT already
	// parked on recvMu AND we are below the maxInflight cap. Skip on
	// Tversion -- handleReVersion drains all inflight and mutates
	// c.msize/c.protocol; a sibling reading with the old protocol
	// mid-renegotiation would corrupt the stream.
	if f.msgType != proto.TypeTversion &&
		c.recvIdle.Load() == 0 &&
		c.workerCount.Load() < int32(c.server.maxInflight) {
		f.spawnReplacement = true
		c.workerCount.Add(1)
		c.recvWG.Add(1)
	}

	// Decode INSIDE recvMu. Branches are mutually exclusive
	// (if/else if/else): unknownType skips decode entirely; Twrite
	// defers buf release (Data aliases buf); other types copy via
	// DecodeFrom and release the buf immediately.
	msg, newMsgErr := c.newMessage(f.msgType)
	f.msg = msg
	switch {
	case newMsgErr != nil && f.msgType != proto.TypeTversion:
		// Unknown message type. Do NOT touch msg (it is nil).
		// Release the buf here; do NOT enter any decode branch.
		f.unknownType = true
		bufpool.PutMsgBuf(bufPtr)
	case f.msgType == proto.TypeTversion:
		// Tversion is special: handleReVersion decodes its own body
		// to avoid allocating a msg struct that it immediately
		// throws away after draining inflight and clunking all fids.
		//
		// Keep bufPtr; the caller releases it after handleReVersion.
		f.bufPtr = bufPtr
		f.body = b[3:]
	default:
		if tw, ok := msg.(*proto.Twrite); ok {
			if err := tw.DecodeFromBuf(b[3:]); err != nil {
				f.decodeErr = err
				// Twrite decode failed before aliasing took effect:
				// release buf now; the caller returns the cached msg on
				// the decodeErr path outside the lock.
				bufpool.PutMsgBuf(bufPtr)
			} else {
				// Successful Twrite: Data aliases bufPtr; defer
				// release to dispatchInline.
				f.deferredBufPtr = bufPtr
			}
			break
		}
		var bodyReader bytes.Reader
		bodyReader.Reset(b[3:])
		if err := msg.DecodeFrom(&bodyReader); err != nil {
			f.decodeErr = err
		}
		// DecodeFrom copied; safe to release immediately
		// (regardless of decodeErr).
		bufpool.PutMsgBuf(bufPtr)
	}

	return f, true
}

// shutdownRecvLocked marks the receive path shut down and wakes the
// cleanup goroutine. The caller must hold recvMu; it is released here.
// Siblings observe recvShutdown at their next lock acquisition.
func (c *conn) shutdownRecvLocked() {
	c.recvShutdown = true
	c.recvMu.Unlock()
	c.signalRecvShutdown()
}

// shutdownRecv is shutdownRecvLocked for callers that do not hold recvMu.
// It additionally closes the conn: recvShutdown alone only catches
// siblings at their next lock acquisition, while closing nc fast-paths any
// sibling already blocked inside a Read syscall. net.Conn.Close is
// idempotent (returns ErrClosed, which is ignored); the redundant close is
// intentional belt-and-braces.
func (c *conn) shutdownRecv() {
	c.recvMu.Lock()
	c.recvShutdown = true
	c.recvMu.Unlock()
	c.signalRecvShutdown()
	_ = c.nc.Close()
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
// still references the recycled buffer.
func (c *conn) dispatchInline(rctx *requestCtx, tag proto.Tag, msg proto.Message, bufPtr *[]byte) {
	finished := false
	finish := func() {
		if !finished {
			c.inflight.finish(tag)
			finished = true
		}
	}

	// LIFO: registered FIRST so it runs LAST - after the tag has been
	// removed from the inflight map (by remove() on the success path or
	// finish() in the deferred fallback). A concurrent Tflush must be able
	// to look up `tag` in the inflight map until then; only once the tag is
	// gone is it safe to recycle rctx back to the pool. Violating this
	// ordering causes Tflush to call flush() on a pool-recycled requestCtx
	// belonging to an unrelated later request.
	defer putRequestCtx(rctx)

	defer func() {
		// Recover BEFORE releasing msg: the log line below reads from msg,
		// and once putCachedMsg runs a concurrent borrower may hold it.
		if r := recover(); r != nil {
			// SERV-06: Handler panic -> EIO, never crash the server.
			c.logger.Error("handler panic",
				slog.Any("panic", r),
				slog.String("message_type", msg.Type().String()),
			)
			c.otelInst.recordAbnormalEvent(reasonHandlerPanic)
			c.sendResponse(tag, c.errorMsg(proto.EIO))
		}
		if bufPtr != nil {
			bufpool.PutMsgBuf(bufPtr)
		}
		// MUST run after PutMsgBuf (source order within this func matters).
		putCachedMsg(msg)
		// Fallback for the resp == nil and handler-panic paths, where the
		// success path below did not already remove the tag. finish() both
		// removes the tag and closes the done channel; on those paths there
		// is no separate write to order a waiting Tflush after.
		finish()
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
		// Commit the tag BEFORE the write: this marks the entry so a
		// legitimately reused tag (start treats a committed entry as free)
		// does not collide with it, while keeping the entry in the map so
		// a Tflush arriving during the write still finds it and blocks on
		// done instead of returning Rflush immediately -- which could
		// otherwise win the race for writeMu and overtake this response on
		// the wire. completeCommit runs AFTER the write and returns
		// whatever done channel exists at that point (a Tflush may have
		// registered one at any time up to this call), which is then
		// closed, releasing a waiter only once the flushed response (if
		// any) is actually on the wire. finished=true makes the deferred
		// finish() above a no-op for this path.
		c.inflight.commit(tag)
		finished = true
		c.sendResponseInline(tag, resp, release)
		if doneCh := c.inflight.completeCommit(tag, rctx); doneCh != nil {
			close(doneCh)
		}
	}
}

// handleReVersion handles a Tversion message received mid-connection. Per the
// 9P spec, Tversion aborts all outstanding I/O and clunks all fids, then
// re-negotiates the protocol version and msize.
func (c *conn) handleReVersion(_ context.Context, tag proto.Tag, body []byte) {
	// Rate-limit re-negotiation attempts. Excessive attempts are dropped
	// without a response to prevent amplification attacks and
	// unnecessary drain/clunk cycles.
	now := time.Now().UnixNano()
	last := c.lastNegotiation.Load()
	if now-last < int64(negotiationRateLimit) {
		c.logger.Debug("re-negotiation rate-limited", slog.Int64("tag", int64(tag)))
		return
	}
	c.lastNegotiation.Store(now)

	// Cancel all inflight request contexts first, then wait for handlers
	// to drain with a deadline before mutating connection state. This
	// ensures no handler goroutine reads c.msize or c.protocol while we
	// are updating them.
	c.inflight.cancelAll()
	drainCtx, drainCancel := context.WithTimeout(context.Background(), cleanupDeadline)
	defer drainCancel()
	if err := c.inflight.waitWithDeadline(drainCtx); err != nil {
		// Drain failed: at least one handler ignored ctx cancellation.
		// The 9P spec requires Tversion to abort all outstanding I/O; if
		// we cannot, continuing would let the late handler write a stale
		// response into the new tag space (tag-reuse aliasing) or read
		// c.msize/c.protocol mid-mutation. Close the connection
		// instead and let the client reconnect cleanly.
		c.logger.Warn("re-negotiation: inflight drain timed out; closing connection",
			slog.Int("remaining", c.inflight.len()),
		)
		c.otelInst.recordAbnormalEvent(reasonForcedClose)
		_ = c.nc.Close() // Fatal error policy
		return
	}

	// Clunk all fids and release handles/closers (matching cleanup pattern).
	states := c.fids.clunkAll()
	if len(states) > 0 {
		c.otelInst.recordFidChange(-int64(len(states)))
	}
	for _, fs := range states {
		lastRef := decRefNode(fs.currentNode())
		fs.releaseNow(context.Background(), c.logger, lastRef)
	}

	var tver proto.Tversion
	if err := tver.DecodeFrom(bytes.NewReader(body)); err != nil {
		c.logger.Warn("re-negotiation decode error; closing connection", slog.Any("error", err))
		_ = c.nc.Close() // Fatal error policy
		return
	}

	// Validate msize + select protocol via shared helper (shared helper).
	res, err := c.negotiate(&tver)
	if err != nil {
		c.logger.Warn("re-negotiation msize too small; closing connection",
			slog.Uint64("msize", uint64(tver.Msize)),
			slog.Any("error", err),
		)
		_ = c.nc.Close() // Fatal error policy
		return
	}

	// Send Rversion directly via writeRaw, which acquires writeMu to
	// prevent interleaving with other dispatchers' writes.
	rver := &proto.Rversion{Msize: res.msize, Version: res.version}
	if err := c.writeRaw(tag, rver); err != nil {
		c.logger.Warn("re-negotiation send error; closing connection", slog.Any("error", err))
		_ = c.nc.Close() // Fatal error policy
		return
	}

	if res.selected == protocolNone {
		return
	}

	c.msize = res.msize
	c.protocol = res.selected
}

// newMessage returns a zero-value message struct for the given type based on
// the negotiated protocol. Shared base requests are accepted for every
// negotiated dialect; dialect-specific requests are accepted only for their
// dialect.
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
		return tremoveCache.Get(), nil
	}

	switch c.protocol {
	case protocolL:
		return newLMessage(t)
	case protocolU:
		return newUMessage(t)
	default:
		return nil, fmt.Errorf("unknown message type %d", t)
	}
}

// newLMessage returns a 9P2000.L-specific request message.
func newLMessage(t proto.MessageType) (proto.Message, error) {
	switch t {
	case proto.TypeTlopen:
		return tlopenCache.Get(), nil
	case proto.TypeTlcreate:
		return tlcreateCache.Get(), nil
	case proto.TypeTgetattr:
		return tgetattrCache.Get(), nil
	case proto.TypeTsetattr:
		return tsetattrCache.Get(), nil
	case proto.TypeTreaddir:
		return &p9l.Treaddir{}, nil
	case proto.TypeTmkdir:
		return tmkdirCache.Get(), nil
	case proto.TypeTsymlink:
		return tsymlinkCache.Get(), nil
	case proto.TypeTlink:
		return &p9l.Tlink{}, nil
	case proto.TypeTmknod:
		return tmknodCache.Get(), nil
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
		return trenameCache.Get(), nil
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

// newUMessage returns a 9P2000.u-specific request message.
func newUMessage(t proto.MessageType) (proto.Message, error) {
	switch t {
	case proto.TypeTopen:
		return &p9u.Topen{}, nil
	case proto.TypeTcreate:
		return &p9u.Tcreate{}, nil
	case proto.TypeTstat:
		return &p9u.Tstat{}, nil
	case proto.TypeTwstat:
		return &p9u.Twstat{}, nil
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
			// The client is still waiting on this tag. Dropping it
			// silently would hang the client forever instead of
			// surfacing the failure, so report EIO in its place.
			c.sendError(tag, proto.EIO)
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
		c.sendError(tag, proto.EIO)
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
	// Re-slice from the conn-resident backing array on every call:
	// wire.WriteFramesLocked (via net.Buffers.WriteTo's v.consume)
	// zeroes both length and capacity of bufs on full consumption, so
	// a hoisted field-level net.Buffers would silently drop subsequent
	// frames. See internal/wire.WriteFramesLocked godoc. writeMu is
	// held above; WriteFramesLocked itself takes no lock (the *Locked
	// suffix advertises the caller contract).
	bufs := net.Buffers(c.encBufsArr[:n])
	err := wire.WriteFramesLocked(c.nc, &bufs)
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
		// A failed write means the connection is broken. Signal shutdown so
		// cleanup tears it down instead of silently dropping responses until
		// an unrelated read failure (a write-half-closed peer whose reads
		// still block would otherwise linger forever with idleTimeout unset).
		c.signalRecvShutdown()
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

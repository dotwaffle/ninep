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

	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

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
// which must be returned to the pool after wire encoding completes.
// Currently used by pooledRread and pooledRreaddir in bridge.go.
// sendResponseInline calls Release after the writev completes.
type releaser interface {
	Release()
}

// workItem is a decoded request ready for handler dispatch. The readLoop
// populates it and sends to conn.workCh; an idle worker picks it up and
// runs the handler chain synchronously, then loops back for the next item.
//
// bufPtr, when non-nil, points at a pooled message body buffer that the
// request aliases (currently only Twrite.Data). handleWorkItem returns
// it to bufpool after the handler completes — storing the pointer directly
// instead of wrapping it in a closure avoids a per-request heap alloc.
type workItem struct {
	ctx    context.Context
	tag    proto.Tag
	msg    proto.Message
	bufPtr *[]byte
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

	// writeMu serializes all writes to nc. Workers acquire it in
	// sendResponseInline, and writeRaw (used during version negotiation)
	// takes it as well. This prevents interleaved wire frames (GO-CC-3).
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

	// hdrBuf is reused for reading 4-byte frame size headers from nc.
	// Stored on conn (heap-allocated) so hdrBuf[:] does not escape to
	// the heap on each readLoop iteration.
	hdrBuf [4]byte

	// bodyReader is reused for wrapping the pooled message body buffer
	// as an io.Reader for DecodeFrom. Reset() instead of bytes.NewReader
	// avoids a per-message allocation.
	bodyReader bytes.Reader

	// inflight tracks per-request goroutines for flush cancellation and
	// drain-on-disconnect.
	inflight *inflightMap

	// Worker pool (lazy-spawn, bounded by maxInflight). Replaces the
	// previous goroutine-per-request model. Workers compete for items
	// on workCh, stay alive for the connection lifetime, and are spawned
	// on demand when no idle worker is available and we're still under
	// cap.
	workCh      chan workItem
	workerCount atomic.Int32   // total workers alive
	idleCount   atomic.Int32   // workers currently waiting on workCh
	workerWG    sync.WaitGroup // tracks workers for cleanup drain

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
		server:   s,
		nc:       nc,
		fids:     newFidTable(),
		maxFids:  s.maxFids,
		inflight: newInflightMap(),
		workCh:   make(chan workItem, s.maxInflight),
		logger:   s.logger.With(slog.String("remote", nc.RemoteAddr().String())),
	}
	// Build the middleware-wrapped dispatch chain. The closure captures c so
	// it must be created after c is initialized. If no middleware is
	// configured, chain returns the inner handler directly (zero overhead).
	inner := func(ctx context.Context, tag proto.Tag, msg proto.Message) proto.Message {
		return c.dispatch(ctx, tag, msg)
	}

	// If OTel providers are configured, prepend OTel middleware (outermost)
	// and create connection-level gauge instruments.
	mws := s.middlewares
	if s.tracerProvider != nil || s.meterProvider != nil {
		tp := s.tracerProvider
		if tp == nil {
			tp = tracenoop.NewTracerProvider()
		}
		mp := s.meterProvider
		if mp == nil {
			mp = metricnoop.NewMeterProvider()
		}
		mws = append([]Middleware{newOTelMiddleware(tp, mp, c)}, mws...)
		c.otelInst = newConnOTelInstruments(s.meterProvider)
	}

	c.handler = chain(inner, mws)
	return c
}

// serve runs the connection lifecycle: version negotiation, then read loop.
// It blocks until the connection is closed or the context is cancelled.
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

	// Drive the read loop synchronously on the serve goroutine. readLoop
	// decodes requests and hands each one to a lazy-spawned worker via
	// workCh; workers encode and writev responses inline under writeMu via
	// sendResponseInline. There is no dedicated writer goroutine and no
	// response channel (removed in v1.1.15). readLoop returns when the
	// connection closes or ctx is done (GO-CC-1).
	c.readLoop(ctx)

	// Orderly shutdown: cancel inflight, drain with deadline, close workCh,
	// wait for workers, clunk fids. Workers encode and writev responses
	// inline under writeMu, so there is no writer goroutine to drain
	// afterwards.
	c.cleanup()
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
// the worker pool's sendResponseInline path. Used during version
// negotiation (both initial and mid-connection re-negotiation) where the
// codec may not yet be selected. Acquires writeMu to serialize writes
// across workers and the raw negotiation path.
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

// readLoop reads and dispatches messages until the connection closes or
// ctx is done. After returning, the caller (serve) invokes cleanup(),
// which cancels inflight requests, waits for them to drain with a
// deadline, closes workCh, waits on workerWG, and clunks all remaining
// fids. Responses are encoded and writev'd inline by workers under
// writeMu (sendResponseInline), so there is no separate writer goroutine
// or response channel to drain.
func (c *conn) readLoop(ctx context.Context) {
	for {
		// Check context before blocking on read.
		if ctx.Err() != nil {
			return
		}

		// Set read deadline for idle timeout (GO-SEC-1).
		if c.server.idleTimeout > 0 {
			if err := c.nc.SetReadDeadline(time.Now().Add(c.server.idleTimeout)); err != nil {
				c.logger.Warn("failed to set read deadline", slog.Any("error", err))
				return
			}
		}

		// Read message size. Uses conn's hdrBuf to avoid heap escape of
		// the temp buffer through the io.Reader interface.
		if _, err := io.ReadFull(c.nc, c.hdrBuf[:]); err != nil {
			if !isExpectedCloseError(err) {
				c.logger.Debug("read error", slog.Any("error", err))
			}
			return
		}
		size := binary.LittleEndian.Uint32(c.hdrBuf[:])
		if size < uint32(proto.HeaderSize) {
			c.logger.Warn("message too small", slog.Uint64("size", uint64(size)))
			return
		}
		if size > c.msize {
			c.logger.Warn("message exceeds msize",
				slog.Uint64("size", uint64(size)),
				slog.Uint64("msize", uint64(c.msize)),
			)
			return
		}

		// Read remaining bytes: type[1] + tag[2] + body.
		//
		// SAFETY (RESEARCH Pattern 4, re-verified in Task 3): every
		// DecodeFrom method in proto/, proto/p9l/, proto/p9u/ that reads
		// []byte or string fields does so via make+copy (e.g. Rread.Data,
		// Twrite.Data, Rreaddir.Data) or via ReadString (which now has
		// its own pooled scratch + unavoidable string() copy). None of
		// them alias the input buffer into message fields. This makes it
		// safe to return buf to the pool AFTER DecodeFrom completes but
		// BEFORE launching the handler goroutine -- msg is fully
		// independent of buf at that point. -race CI catches regressions
		// if a future DecodeFrom introduces aliasing.
		bufPtr := bufpool.GetMsgBuf(int(size - 4))
		b := (*bufPtr)[:size-4]
		if _, err := io.ReadFull(c.nc, b); err != nil {
			bufpool.PutMsgBuf(bufPtr)
			if !isExpectedCloseError(err) {
				c.logger.Debug("read body error", slog.Any("error", err))
			}
			return
		}

		// Parse header.
		msgType := proto.MessageType(b[0])
		tag := proto.Tag(binary.LittleEndian.Uint16(b[1:3]))

		// Handle Tversion mid-connection (re-negotiation).
		// handleReVersion uses body synchronously (DecodeFrom copies); after
		// it returns, the pool buffer is safe to release.
		if msgType == proto.TypeTversion {
			c.handleReVersion(ctx, tag, b[3:])
			bufpool.PutMsgBuf(bufPtr)
			continue
		}

		// Handle Tflush synchronously in the read loop to avoid deadlock:
		// if all semaphore slots are taken, Tflush must still execute to
		// cancel a pending request and free a slot (T-02-10).
		if msgType == proto.TypeTflush {
			var tf proto.Tflush
			if err := tf.DecodeFrom(bytes.NewReader(b[3:])); err != nil {
				bufpool.PutMsgBuf(bufPtr)
				c.logger.Warn("decode tflush error", slog.Any("error", err))
				return
			}
			// tf has no reference into b after DecodeFrom. Safe to Put.
			bufpool.PutMsgBuf(bufPtr)
			resp := c.handleFlush(ctx, &tf)
			c.sendResponse(tag, resp)
			continue
		}

		// Decode message body via protocol-specific message factory.
		msg, err := c.newMessage(msgType)
		if err != nil {
			bufpool.PutMsgBuf(bufPtr)
			// Unknown message type -> ENOSYS.
			c.sendError(tag, proto.ENOSYS)
			continue
		}

		// deferredBufPtr is the pooled message-body buffer carried over
		// to the handler when the request aliases into it (currently only
		// Twrite with DecodeFromBuf). nil for messages whose DecodeFrom
		// copies all data out — the buffer is returned to the pool
		// immediately. Carrying a raw pointer avoids the per-request
		// closure alloc that wrapping PutMsgBuf in func() would cost.
		var deferredBufPtr *[]byte

		if tw, ok := msg.(*proto.Twrite); ok {
			// Zero-copy Twrite: m.Data aliases bufPtr; defer release.
			if err := tw.DecodeFromBuf(b[3:]); err != nil {
				bufpool.PutMsgBuf(bufPtr)
				c.logger.Warn("decode error",
					slog.String("type", msgType.String()),
					slog.Any("error", err),
				)
				return // Fatal decode error.
			}
			deferredBufPtr = bufPtr
			// Do NOT put bufPtr to the pool here — handleWorkItem's
			// defer returns it after the handler finishes with Data.
		} else {
			c.bodyReader.Reset(b[3:])
			if err := msg.DecodeFrom(&c.bodyReader); err != nil {
				bufpool.PutMsgBuf(bufPtr)
				c.logger.Warn("decode error",
					slog.String("type", msgType.String()),
					slog.Any("error", err),
				)
				return // Fatal decode error.
			}
			// DecodeFrom copied buf contents into msg fields; msg is
			// independent of bufPtr. Safe to return immediately.
			bufpool.PutMsgBuf(bufPtr)
		}

		// Create per-request context with cancellation for flush support.
		reqCtx, cancel := context.WithCancel(ctx)
		c.inflight.start(tag, cancel)

		// Spawn a worker if nobody is idle and we're under the maxInflight
		// cap. Lazy spawn keeps the worker pool sized to actual concurrency
		// demand; existing workers stay alive for the connection lifetime.
		if c.idleCount.Load() == 0 && c.workerCount.Load() < int32(c.server.maxInflight) {
			c.workerCount.Add(1)
			c.workerWG.Add(1)
			go c.worker(ctx)
		}

		// Dispatch. Blocks if workCh is full (all workers busy + cap
		// reached), providing the same back-pressure the old semaphore
		// offered. On ctx cancellation, clean up the request state.
		select {
		case c.workCh <- workItem{ctx: reqCtx, tag: tag, msg: msg, bufPtr: deferredBufPtr}:
		case <-ctx.Done():
			if deferredBufPtr != nil {
				bufpool.PutMsgBuf(deferredBufPtr)
			}
			c.inflight.finish(tag)
			return
		}
	}
}

// worker is a long-lived goroutine that processes request work items from
// workCh until the channel is closed (by cleanup). Panic recovery is
// per-request (inside handleWorkItem) so one bad handler does not
// terminate the worker. connCtx is accepted for symmetry with future
// uses but not needed for exit — cleanup always runs after readLoop and
// unconditionally closes workCh.
func (c *conn) worker(_ context.Context) {
	defer c.workerWG.Done()
	defer c.workerCount.Add(-1)

	for {
		c.idleCount.Add(1)
		item, ok := <-c.workCh
		c.idleCount.Add(-1)
		if !ok {
			return // channel closed by cleanup
		}
		c.handleWorkItem(item)
	}
}

// handleWorkItem runs one request through the middleware + dispatch chain
// with panic recovery. Invoked synchronously by worker(); a panic is
// caught, turned into an EIO response, and the worker loop continues.
// item.bufPtr, when non-nil, is a pooled message-body buffer the request
// aliases (e.g. Twrite.Data); it is returned to bufpool after the handler
// finishes with it.
func (c *conn) handleWorkItem(item workItem) {
	defer func() {
		if item.bufPtr != nil {
			bufpool.PutMsgBuf(item.bufPtr)
		}
		// Return the request struct to its type-specific cache (if any)
		// for reuse by a later request — bounded, non-blocking, no-op if
		// the cache is full. Must happen AFTER bufPtr release because
		// Twrite.Data aliases that buffer and putCachedMsg clears it.
		putCachedMsg(item.msg)
		if r := recover(); r != nil {
			// SERV-06: Handler panic -> EIO, never crash the server.
			c.logger.Error("handler panic",
				slog.Any("panic", r),
				slog.String("message_type", item.msg.Type().String()),
			)
			c.sendResponse(item.tag, c.errorMsg(proto.EIO))
		}
		c.inflight.finish(item.tag)
	}()

	resp := c.handler(item.ctx, item.tag, item.msg)
	if resp != nil {
		// Store the releaser interface verbatim on taggedResponse — taking
		// r.Release as a method value would allocate a heap closure on
		// every request. Passing the interface value costs no extra alloc
		// and sendResponseInline invokes release.Release() virtually.
		var release releaser
		if r, ok := resp.(releaser); ok {
			release = r
		}
		c.sendResponseInline(item.tag, resp, release)
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
	// prevent interleaving with other workers' writes (CR-01).
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
		return getCachedTwalk(), nil
	case proto.TypeTclunk:
		return getCachedTclunk(), nil
	case proto.TypeTflush:
		return &proto.Tflush{}, nil
	case proto.TypeTauth:
		return &proto.Tauth{}, nil
	case proto.TypeTread:
		return getCachedTread(), nil
	case proto.TypeTwrite:
		return getCachedTwrite(), nil
	case proto.TypeTremove:
		return &proto.Tremove{}, nil

	// 9P2000.L-specific message types for capability bridge.
	case proto.TypeTlopen:
		return getCachedTlopen(), nil
	case proto.TypeTlcreate:
		return &p9l.Tlcreate{}, nil
	case proto.TypeTgetattr:
		return getCachedTgetattr(), nil
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
// directly from the worker goroutine. Responses are encoded and writev'd
// inline by the worker that handled the request (under writeMu), so there
// is no inter-goroutine handoff between the worker and a dedicated writer
// — saves one context switch per request (~1-3 μs) on the small-file hot
// path (v1.1.15).
//
// Serialises concurrent worker writes via writeMu, and uses the
// conn-resident encHdr/encBufsArr buffers (guarded by writeMu) to
// avoid per-response allocation. On TCP / unix-domain sockets the write
// is a single writev syscall covering header + body (+ optional Payloader
// payload).
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

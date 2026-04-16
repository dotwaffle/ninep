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

// taggedResponse pairs a tag with a response for the writer goroutine.
// The optional release func is called after the response is encoded; it
// returns pooled buffers (e.g. bridge Rread/Rreaddir data buffers) to
// their pools.
type taggedResponse struct {
	tag     proto.Tag
	msg     proto.Message
	release func() // called after encode; nil when no pooled buffer is held
}

// releaser is implemented by response messages that carry pooled buffers
// which must be returned to the pool after wire encoding completes.
// Currently used by pooledRread and pooledRreaddir in bridge.go.
type releaser interface {
	Release()
}

// workItem is a decoded request ready for handler dispatch. The readLoop
// populates it and sends to conn.workCh; an idle worker picks it up and
// runs the handler chain synchronously, then loops back for the next item.
type workItem struct {
	ctx     context.Context
	tag     proto.Tag
	msg     proto.Message
	release func() // called after handler returns; nil when no pooled buffer held
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

	// writeMu serializes all writes to nc. Both writeRaw (used during
	// version negotiation) and writeLoop write to the same net.Conn;
	// this mutex prevents interleaved wire frames (GO-CC-3).
	writeMu sync.Mutex

	// hdrBuf is reused for reading 4-byte frame size headers from nc.
	// Stored on conn (heap-allocated) so hdrBuf[:] does not escape to
	// the heap on each readLoop iteration.
	hdrBuf [4]byte

	// bodyReader is reused for wrapping the pooled message body buffer
	// as an io.Reader for DecodeFrom. Reset() instead of bytes.NewReader
	// avoids a per-message allocation.
	bodyReader bytes.Reader

	// Write coalescing: the writeLoop drains pending responses non-
	// blockingly and writev()s them in a single syscall. Slabs are
	// sized to maxInflight (the upper bound on queued responses) so
	// batching never needs to allocate.
	//
	//   hdrSlab:   raw bytes for per-response 7-byte headers
	//   encBufs:   [][]byte alternating hdr/body, 2*maxInflight entries
	//   bodies:    pooled body buffers for post-write release tracking
	//   batch:     captured taggedResponse entries, for release callbacks
	hdrSlab []byte
	encBufs net.Buffers
	bodies  []*bytes.Buffer
	batch   []taggedResponse

	// responses carries encoded responses to the writeLoop goroutine.
	// The sender (readLoop/dispatch) closes this channel; the writer drains it.
	responses chan taggedResponse

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
		server:    s,
		nc:        nc,
		fids:      newFidTable(),
		maxFids:   s.maxFids,
		responses: make(chan taggedResponse, s.maxInflight),
		inflight:  newInflightMap(),
		// Coalescing slabs sized to maxInflight responses.
		hdrSlab: make([]byte, s.maxInflight*int(proto.HeaderSize)),
		encBufs: make(net.Buffers, 0, 2*s.maxInflight),
		bodies:  make([]*bytes.Buffer, 0, s.maxInflight),
		batch:   make([]taggedResponse, 0, s.maxInflight),
		workCh:    make(chan workItem, s.maxInflight),
		logger:    s.logger.With(slog.String("remote", nc.RemoteAddr().String())),
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

	// Start writer goroutine. writeLoop exits when c.responses is closed
	// (by cleanup) or ctx is done (GO-CC-1).
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		c.writeLoop(ctx)
	}()

	c.readLoop(ctx)

	// Orderly shutdown: cancel inflight, drain with deadline, clunk fids,
	// close responses channel (which terminates the writer).
	c.cleanup()

	// Wait for the writer to drain and exit.
	<-writerDone
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

// writeRaw encodes a single message directly to the connection, bypassing the
// writeLoop. Used during version negotiation (both initial and mid-connection
// re-negotiation). Acquires writeMu to prevent interleaving with writeLoop.
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

// readLoop reads and dispatches messages until the connection closes or ctx is
// done. After returning, the caller must close c.responses.
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

		// msgRelease is called after the handler returns (in handleRequest
		// defer). nil for messages whose DecodeFrom copies all data out of
		// the pool buffer — the buffer is returned to the pool immediately.
		// Non-nil for Twrite (DecodeFromBuf aliases m.Data into bufPtr) so
		// the buffer survives handler execution.
		var msgRelease func()

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
			bp := bufPtr
			msgRelease = func() { bufpool.PutMsgBuf(bp) }
			// Do NOT put bufPtr to the pool here — it is released by
			// handleRequest's defer after the handler finishes with Data.
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
		case c.workCh <- workItem{ctx: reqCtx, tag: tag, msg: msg, release: msgRelease}:
		case <-ctx.Done():
			if msgRelease != nil {
				msgRelease()
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
// release, when non-nil, returns any pooled buffers aliased by the request
// (e.g. Twrite.Data) after the handler returns.
func (c *conn) handleWorkItem(item workItem) {
	defer func() {
		if item.release != nil {
			item.release()
		}
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
		var release func()
		if r, ok := resp.(releaser); ok {
			release = r.Release
		}
		c.sendResponseWithRelease(item.tag, resp, release)
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
	// prevent interleaving with the writeLoop goroutine (CR-01).
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
		return &proto.Twalk{}, nil
	case proto.TypeTclunk:
		return &proto.Tclunk{}, nil
	case proto.TypeTflush:
		return &proto.Tflush{}, nil
	case proto.TypeTauth:
		return &proto.Tauth{}, nil
	case proto.TypeTread:
		return &proto.Tread{}, nil
	case proto.TypeTwrite:
		return &proto.Twrite{}, nil
	case proto.TypeTremove:
		return &proto.Tremove{}, nil

	// 9P2000.L-specific message types for capability bridge.
	case proto.TypeTlopen:
		return &p9l.Tlopen{}, nil
	case proto.TypeTlcreate:
		return &p9l.Tlcreate{}, nil
	case proto.TypeTgetattr:
		return &p9l.Tgetattr{}, nil
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

// writeLoop drains the responses channel, encodes responses to the
// connection, and coalesces pending responses into a single writev() syscall
// where possible. It exits when the channel is closed or ctx is done.
//
// Coalescing: when a response arrives, writeLoop drains any additional
// pending responses non-blockingly (up to maxInflight), then issues one
// net.Buffers.WriteTo for the entire batch. On TCP/unix-domain sockets
// this maps to a single writev() syscall; net.Pipe still falls back to
// sequential Writes internally.
func (c *conn) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case resp, ok := <-c.responses:
			if !ok {
				return
			}
			c.flushBatch(resp)
		}
	}
}

// flushBatch collects resp plus any additional pending responses,
// encodes them all, and writes them in a single net.Buffers.WriteTo.
func (c *conn) flushBatch(first taggedResponse) {
	// Reset reusable batch state (keeps backing arrays).
	c.batch = c.batch[:0]
	c.bodies = c.bodies[:0]
	c.encBufs = c.encBufs[:0]

	// Seed with first response.
	c.batch = append(c.batch, first)

	// Drain additional pending responses non-blockingly. Bound by the
	// pre-allocated slab size so we never reallocate; the channel size
	// itself is bounded by maxInflight.
	maxBatch := cap(c.batch)
drainLoop:
	for len(c.batch) < maxBatch {
		select {
		case resp, ok := <-c.responses:
			if !ok {
				break drainLoop
			}
			c.batch = append(c.batch, resp)
		default:
			break drainLoop
		}
	}

	// Encode each response: fixed body bytes into a pooled buffer, header
	// into the hdrSlab slot. Messages implementing proto.Payloader have
	// their payload placed as a separate net.Buffers entry so the bytes
	// go from the response struct directly to the socket via writev —
	// skipping the copy into the pooled body buffer for large Rread/Rreaddir
	// responses.
	for i, resp := range c.batch {
		body := bufpool.GetBuf()

		// Payloader fast path: encode only the fixed part (e.g. 4-byte
		// count for Rread) into body; keep the payload slice as a
		// separate net.Buffers entry.
		var payload []byte
		if pl, ok := resp.msg.(proto.Payloader); ok {
			if err := pl.EncodeFixed(body); err != nil {
				c.logger.Warn("encode error",
					slog.String("type", resp.msg.Type().String()),
					slog.Any("error", err),
				)
				bufpool.PutBuf(body)
				if resp.release != nil {
					resp.release()
				}
				c.bodies = append(c.bodies, nil)
				continue
			}
			payload = pl.Payload()
		} else if err := resp.msg.EncodeTo(body); err != nil {
			c.logger.Warn("encode error",
				slog.String("type", resp.msg.Type().String()),
				slog.Any("error", err),
			)
			bufpool.PutBuf(body)
			if resp.release != nil {
				resp.release()
			}
			c.bodies = append(c.bodies, nil)
			continue
		}
		c.bodies = append(c.bodies, body)

		size := uint32(proto.HeaderSize) + uint32(body.Len()) + uint32(len(payload))
		hdrSlot := c.hdrSlab[i*int(proto.HeaderSize) : (i+1)*int(proto.HeaderSize)]
		binary.LittleEndian.PutUint32(hdrSlot[0:4], size)
		hdrSlot[4] = uint8(resp.msg.Type())
		binary.LittleEndian.PutUint16(hdrSlot[5:7], uint16(resp.tag))
		c.encBufs = append(c.encBufs, hdrSlot, body.Bytes())
		if len(payload) > 0 {
			c.encBufs = append(c.encBufs, payload)
		}
	}

	// Single writev under writeMu.
	c.writeMu.Lock()
	if c.server.idleTimeout > 0 {
		if err := c.nc.SetWriteDeadline(time.Now().Add(c.server.idleTimeout)); err != nil {
			c.writeMu.Unlock()
			c.logger.Warn("failed to set write deadline", slog.Any("error", err))
			// Still release all batch resources on failure.
			c.releaseBatch()
			return
		}
	}
	_, err := c.encBufs.WriteTo(c.nc)
	c.writeMu.Unlock()

	// Release all pooled body buffers and run all release callbacks.
	c.releaseBatch()

	if err != nil {
		c.logger.Warn("write error",
			slog.Int("batch_size", len(c.batch)),
			slog.Any("error", err),
		)
	}
}

// releaseBatch returns pooled body buffers and calls per-response release
// callbacks after a batched write completes (or fails).
func (c *conn) releaseBatch() {
	for i, resp := range c.batch {
		if i < len(c.bodies) && c.bodies[i] != nil {
			bufpool.PutBuf(c.bodies[i])
		}
		if resp.release != nil {
			resp.release()
		}
	}
}

// sendResponse queues a response for the writer goroutine. The send blocks
// until the writeLoop drains the channel, ensuring clients always receive
// their replies. If the responses channel has been closed (connection cleanup
// completed), the send panics and is recovered -- this handles the case where
// a stuck handler outlasts the cleanup deadline.
func (c *conn) sendResponse(tag proto.Tag, msg proto.Message) {
	c.sendResponseWithRelease(tag, msg, nil)
}

// sendResponseWithRelease queues a response with an optional release func
// that is called by the writeLoop after the response has been encoded on
// the wire. Used for pooled-buffer responses (e.g. Rread, Rreaddir).
// If the responses channel has been closed (cleanup complete), the send
// panics and the release runs during recovery so pool buffers are not
// leaked when the handler outlives the connection.
func (c *conn) sendResponseWithRelease(tag proto.Tag, msg proto.Message, release func()) {
	defer func() {
		if r := recover(); r != nil {
			// Channel was closed by cleanup -- drop the response, but
			// still return any pooled buffers to avoid leaks.
			if release != nil {
				release()
			}
			c.logger.Debug("response dropped after cleanup",
				slog.String("type", msg.Type().String()),
			)
		}
	}()

	c.responses <- taggedResponse{tag: tag, msg: msg, release: release}
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

package server

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

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
	protocolL    // 9P2000.L
	protocolU    // 9P2000.u
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

// taggedResponse pairs a tag with a response for the writer goroutine.
type taggedResponse struct {
	tag proto.Tag
	msg proto.Message
}

// conn represents a single client connection to the server.
type conn struct {
	server   *Server
	nc       net.Conn
	fids     *fidTable
	protocol protocol
	msize    uint32 // Negotiated msize (0 until version negotiation).
	codec    codec

	// writeMu serializes all writes to nc. Both writeRaw (used during
	// version negotiation) and writeLoop write to the same net.Conn;
	// this mutex prevents interleaved wire frames (GO-CC-3).
	writeMu sync.Mutex

	// responses carries encoded responses to the writeLoop goroutine.
	// The sender (readLoop/dispatch) closes this channel; the writer drains it.
	responses chan taggedResponse

	// inflight tracks per-request goroutines for flush cancellation and
	// drain-on-disconnect.
	inflight *inflightMap

	// semaphore limits concurrent request goroutines to MaxInflight.
	// A buffered channel of size maxInflight acts as a counting semaphore.
	semaphore chan struct{}

	logger *slog.Logger

	// handler is the middleware-wrapped dispatch chain. Built once in newConn
	// from chain(dispatch, server.middlewares). If no middleware is configured,
	// this is a direct call to dispatch with zero overhead.
	handler Handler
}

// newConn creates a new conn for the given server and network connection.
func newConn(s *Server, nc net.Conn) *conn {
	c := &conn{
		server:    s,
		nc:        nc,
		fids:      newFidTable(),
		responses: make(chan taggedResponse, s.maxInflight),
		inflight:  newInflightMap(),
		semaphore: make(chan struct{}, s.maxInflight),
		logger:    s.logger.With(slog.String("remote", nc.RemoteAddr().String())),
	}
	// Build the middleware-wrapped dispatch chain. The closure captures c so
	// it must be created after c is initialized. If no middleware is
	// configured, chain returns the inner handler directly (zero overhead).
	inner := func(ctx context.Context, tag proto.Tag, msg proto.Message) proto.Message {
		return c.dispatch(ctx, tag, msg)
	}
	c.handler = chain(inner, s.middlewares)
	return c
}

// serve runs the connection lifecycle: version negotiation, then read loop.
// It blocks until the connection is closed or the context is cancelled.
func (c *conn) serve(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer c.nc.Close()

	if err := c.negotiateVersion(ctx); err != nil {
		c.logger.Debug("version negotiation failed", slog.Any("error", err))
		return
	}

	// Inject connection metadata into context for node handlers.
	ctx = withConnInfo(ctx, &ConnInfo{
		Protocol:   c.protocol.String(),
		Msize:      c.msize,
		RemoteAddr: c.nc.RemoteAddr().String(),
	})

	// Close the net.Conn when context is cancelled to unblock reads (GO-CC-2).
	go func() {
		<-ctx.Done()
		c.nc.Close()
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

	// Negotiate msize: min(client, server).
	negotiated := tver.Msize
	if negotiated > c.server.maxMsize {
		negotiated = c.server.maxMsize
	}
	if negotiated < minMsize {
		return ErrMsizeTooSmall
	}

	// Select protocol from version string.
	var selected protocol
	version := tver.Version
	switch version {
	case "9P2000.L":
		selected = protocolL
	case "9P2000.u":
		selected = protocolU
	default:
		// Unknown version: respond with "unknown" per spec, then close.
		version = "unknown"
	}

	// Send Rversion response manually (codec not yet selected for the first response).
	rver := &proto.Rversion{Msize: negotiated, Version: version}
	if err := c.writeRaw(proto.Tag(tag), rver); err != nil {
		return fmt.Errorf("send rversion: %w", err)
	}

	if selected == protocolNone {
		return ErrNotNegotiated
	}

	c.msize = negotiated
	c.protocol = selected
	switch selected {
	case protocolL:
		c.codec = codecL
	case protocolU:
		c.codec = codecU
	}

	c.logger.Debug("version negotiated",
		slog.String("version", version),
		slog.Uint64("msize", uint64(negotiated)),
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

	var body bytes.Buffer
	if err := msg.EncodeTo(&body); err != nil {
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

		// Read message size.
		size, err := proto.ReadUint32(c.nc)
		if err != nil {
			if !isExpectedCloseError(err) {
				c.logger.Debug("read error", slog.Any("error", err))
			}
			return
		}
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
		buf := make([]byte, size-4) // TODO: use sync.Pool in optimization pass
		if _, err := io.ReadFull(c.nc, buf); err != nil {
			if !isExpectedCloseError(err) {
				c.logger.Debug("read body error", slog.Any("error", err))
			}
			return
		}

		// Parse header.
		msgType := proto.MessageType(buf[0])
		tag := proto.Tag(binary.LittleEndian.Uint16(buf[1:3]))

		// Handle Tversion mid-connection (re-negotiation).
		if msgType == proto.TypeTversion {
			c.handleReVersion(ctx, tag, buf[3:])
			continue
		}

		// Handle Tflush synchronously in the read loop to avoid deadlock:
		// if all semaphore slots are taken, Tflush must still execute to
		// cancel a pending request and free a slot (T-02-10).
		if msgType == proto.TypeTflush {
			var tf proto.Tflush
			if err := tf.DecodeFrom(bytes.NewReader(buf[3:])); err != nil {
				c.logger.Warn("decode tflush error", slog.Any("error", err))
				return
			}
			resp := c.handleFlush(ctx, &tf)
			c.sendResponse(tag, resp)
			continue
		}

		// Decode message body via protocol-specific message factory.
		msg, err := c.newMessage(msgType)
		if err != nil {
			// Unknown message type -> ENOSYS.
			c.sendError(tag, proto.ENOSYS)
			continue
		}
		if err := msg.DecodeFrom(bytes.NewReader(buf[3:])); err != nil {
			c.logger.Warn("decode error",
				slog.String("type", msgType.String()),
				slog.Any("error", err),
			)
			return // Fatal decode error.
		}

		// Acquire semaphore slot (blocks if MaxInflight reached).
		select {
		case c.semaphore <- struct{}{}:
		case <-ctx.Done():
			return
		}

		// Create per-request context with cancellation for flush support.
		reqCtx, cancel := context.WithCancel(ctx)
		c.inflight.start(tag, cancel)

		go c.handleRequest(reqCtx, tag, msg)
	}
}

// handleRequest runs a single request in its own goroutine with panic recovery.
// It releases the semaphore slot and clears the inflight entry when done.
func (c *conn) handleRequest(ctx context.Context, tag proto.Tag, msg proto.Message) {
	defer func() {
		if r := recover(); r != nil {
			// SERV-06: Handler panic -> EIO, never crash the server.
			c.logger.Error("handler panic",
				slog.Any("panic", r),
				slog.String("message_type", msg.Type().String()),
			)
			c.sendResponse(tag, c.errorMsg(proto.EIO))
		}
		c.inflight.finish(tag)
		<-c.semaphore // Release semaphore slot.
	}()

	resp := c.handler(ctx, tag, msg)
	if resp != nil {
		c.sendResponse(tag, resp)
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

	negotiated := tver.Msize
	if negotiated > c.server.maxMsize {
		negotiated = c.server.maxMsize
	}
	if negotiated < minMsize {
		c.logger.Warn("re-negotiation msize too small", slog.Uint64("msize", uint64(negotiated)))
		return
	}

	version := tver.Version
	var selected protocol
	switch version {
	case "9P2000.L":
		selected = protocolL
	case "9P2000.u":
		selected = protocolU
	default:
		version = "unknown"
	}

	// Send Rversion directly via writeRaw, which acquires writeMu to
	// prevent interleaving with the writeLoop goroutine (CR-01).
	rver := &proto.Rversion{Msize: negotiated, Version: version}
	if err := c.writeRaw(tag, rver); err != nil {
		c.logger.Warn("re-negotiation send error", slog.Any("error", err))
		return
	}

	if selected == protocolNone {
		return
	}

	c.msize = negotiated
	c.protocol = selected
	switch selected {
	case protocolL:
		c.codec = codecL
	case protocolU:
		c.codec = codecU
	}
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

// writeLoop drains the responses channel and encodes each response to the
// connection. It exits when the channel is closed or ctx is done.
func (c *conn) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case resp, ok := <-c.responses:
			if !ok {
				return
			}
			c.writeMu.Lock()
			// Set write deadline for idle timeout (GO-SEC-1).
			if c.server.idleTimeout > 0 {
				if err := c.nc.SetWriteDeadline(time.Now().Add(c.server.idleTimeout)); err != nil {
					c.writeMu.Unlock()
					c.logger.Warn("failed to set write deadline", slog.Any("error", err))
					return
				}
			}
			err := c.codec.encode(c.nc, resp.tag, resp.msg)
			c.writeMu.Unlock()
			if err != nil {
				c.logger.Warn("write error",
					slog.String("type", resp.msg.Type().String()),
					slog.Any("error", err),
				)
				// Don't kill connection for one bad response.
				continue
			}
		}
	}
}

// sendResponse queues a response for the writer goroutine. The send blocks
// until the writeLoop drains the channel, ensuring clients always receive
// their replies. If the responses channel has been closed (connection cleanup
// completed), the send panics and is recovered -- this handles the case where
// a stuck handler outlasts the cleanup deadline.
func (c *conn) sendResponse(tag proto.Tag, msg proto.Message) {
	defer func() {
		if r := recover(); r != nil {
			// Channel was closed by cleanup -- drop the response.
			c.logger.Debug("response dropped after cleanup",
				slog.String("type", msg.Type().String()),
			)
		}
	}()

	c.responses <- taggedResponse{tag: tag, msg: msg}
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
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

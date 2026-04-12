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

	// responses carries encoded responses to the writeLoop goroutine.
	// The sender (readLoop/dispatch) closes this channel; the writer drains it.
	responses chan taggedResponse

	logger *slog.Logger
}

// newConn creates a new conn for the given server and network connection.
func newConn(s *Server, nc net.Conn) *conn {
	return &conn{
		server:    s,
		nc:        nc,
		fids:      newFidTable(),
		responses: make(chan taggedResponse, s.maxInflight),
		logger:    s.logger.With(slog.String("remote", nc.RemoteAddr().String())),
	}
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

	// Start writer goroutine. The sender (readLoop) closes c.responses when
	// done, which causes writeLoop to drain and exit (GO-CC-1).
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.writeLoop(ctx)
	}()

	c.readLoop(ctx)
	close(c.responses)

	// Wait for the writer to drain and exit.
	<-done
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
// writeLoop. Used during version negotiation before the writer goroutine is
// started.
func (c *conn) writeRaw(tag proto.Tag, msg proto.Message) error {
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

		c.dispatch(ctx, tag, msg)
	}
}

// handleReVersion handles a Tversion message received mid-connection. Per spec,
// this resets all fids and re-negotiates.
func (c *conn) handleReVersion(_ context.Context, tag proto.Tag, body []byte) {
	c.fids.clunkAll()

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

	// Send Rversion directly (not through writeLoop to avoid ordering issues).
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
	default:
		// Delegate to the protocol-specific codec for extension types.
		if c.codec.decode != nil {
			// We cannot use the codec's Decode (it reads from an io.Reader).
			// For now return unknown; Phase 3+ will add protocol-specific types.
			return nil, fmt.Errorf("unknown message type %d", t)
		}
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
			// Set write deadline for idle timeout (GO-SEC-1).
			if c.server.idleTimeout > 0 {
				if err := c.nc.SetWriteDeadline(time.Now().Add(c.server.idleTimeout)); err != nil {
					c.logger.Warn("failed to set write deadline", slog.Any("error", err))
					return
				}
			}
			if err := c.codec.encode(c.nc, resp.tag, resp.msg); err != nil {
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

// sendResponse queues a response for the writer goroutine.
func (c *conn) sendResponse(tag proto.Tag, msg proto.Message) {
	select {
	case c.responses <- taggedResponse{tag: tag, msg: msg}:
	default:
		c.logger.Warn("response channel full, dropping response",
			slog.String("type", msg.Type().String()),
		)
	}
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

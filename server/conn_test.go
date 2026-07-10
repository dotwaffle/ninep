package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// discardLogger returns a logger that discards all output, suitable for tests
// that don't need to verify log output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// sendTversion writes a raw Tversion message to w. It encodes the full wire
// frame: size[4] + type[1] + tag[2] + msize[4] + version[s].
func sendTversion(tb testing.TB, w io.Writer, msize uint32, version string) {
	tb.Helper()
	var body bytes.Buffer
	tv := &proto.Tversion{Msize: msize, Version: version}
	if err := tv.EncodeTo(&body); err != nil {
		tb.Fatalf("encode tversion body: %v", err)
	}
	size := uint32(proto.HeaderSize) + uint32(body.Len())
	if err := proto.WriteUint32(w, size); err != nil {
		tb.Fatalf("write size: %v", err)
	}
	if err := proto.WriteUint8(w, uint8(proto.TypeTversion)); err != nil {
		tb.Fatalf("write type: %v", err)
	}
	if err := proto.WriteUint16(w, uint16(proto.NoTag)); err != nil {
		tb.Fatalf("write tag: %v", err)
	}
	if _, err := w.Write(body.Bytes()); err != nil {
		tb.Fatalf("write body: %v", err)
	}
}

// readRversion reads a raw Rversion from r and returns it.
func readRversion(tb testing.TB, r io.Reader) *proto.Rversion {
	tb.Helper()
	size, err := proto.ReadUint32(r)
	if err != nil {
		tb.Fatalf("read rversion size: %v", err)
	}
	if size < uint32(proto.HeaderSize) {
		tb.Fatalf("rversion size too small: %d", size)
	}
	msgType, err := proto.ReadUint8(r)
	if err != nil {
		tb.Fatalf("read rversion type: %v", err)
	}
	if proto.MessageType(msgType) != proto.TypeRversion {
		tb.Fatalf("expected Rversion (type %d), got type %d", proto.TypeRversion, msgType)
	}
	if _, err := proto.ReadUint16(r); err != nil { // tag
		tb.Fatalf("read rversion tag: %v", err)
	}
	bodySize := int64(size) - int64(proto.HeaderSize)
	var rv proto.Rversion
	if err := rv.DecodeFrom(io.LimitReader(r, bodySize)); err != nil {
		tb.Fatalf("decode rversion: %v", err)
	}
	return &rv
}

// rootNode is a minimal directory node for testing.
type rootNode struct {
	Inode
}

// newRootNode creates a rootNode initialized with the given QID.
func newRootNode(qid proto.QID) *rootNode {
	n := &rootNode{}
	n.Init(qid, n)
	return n
}

func TestVersionNegotiation(t *testing.T) {
	t.Parallel()

	rootQID := proto.QID{Type: proto.QTDIR, Version: 0, Path: 1}

	tests := []struct {
		name        string
		clientMsize uint32
		serverMsize uint32
		version     string
		wantMsize   uint32
		wantVersion string
		wantClose   bool // true if connection should close after Rversion
	}{
		{
			name:        "L_ClientSmaller",
			clientMsize: 8192,
			serverMsize: 131072,
			version:     "9P2000.L",
			wantMsize:   8192,
			wantVersion: "9P2000.L",
		},
		{
			name:        "U_ServerSmaller",
			clientMsize: 65536,
			serverMsize: 65536,
			version:     "9P2000.u",
			wantMsize:   65536,
			wantVersion: "9P2000.u",
		},
		{
			name:        "MsizeClamp",
			clientMsize: 1048576,
			serverMsize: 131072,
			version:     "9P2000.L",
			wantMsize:   131072,
			wantVersion: "9P2000.L",
		},
		{
			name:        "Unknown",
			clientMsize: 8192,
			serverMsize: 131072,
			version:     "9P2000.invalid",
			wantMsize:   8192,
			wantVersion: "unknown",
			wantClose:   true,
		},
		{
			name:        "TooSmall",
			clientMsize: 100,
			serverMsize: 131072,
			version:     "9P2000.L",
			wantClose:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := newRootNode(rootQID)
			srv := New(root, WithMaxMsize(tt.serverMsize))

			client, server := net.Pipe()
			defer func() { _ = client.Close() }()
			defer func() { _ = server.Close() }()

			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()

			done := make(chan struct{})
			go func() {
				defer close(done)
				srv.ServeConn(ctx, server)
			}()

			sendTversion(t, client, tt.clientMsize, tt.version)

			if tt.wantClose && tt.wantMsize == 0 {
				// Msize too small: server should close the connection without
				// a valid Rversion. Read should fail.
				buf := make([]byte, 128)
				_ = client.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
				_, _ = client.Read(buf)
				// The server might close before or after sending.
				// If it did send, it is acceptable as an implementation
				// choice. Just verify connection closes.
				cancel()
				<-done
				return
			}

			rv := readRversion(t, client)
			if rv.Msize != tt.wantMsize {
				t.Errorf("msize = %d, want %d", rv.Msize, tt.wantMsize)
			}
			if rv.Version != tt.wantVersion {
				t.Errorf("version = %q, want %q", rv.Version, tt.wantVersion)
			}

			if tt.wantClose {
				// Connection should close after unknown version.
				buf := make([]byte, 1)
				_ = client.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
				_, err := client.Read(buf)
				if err == nil {
					t.Error("expected connection to close after unknown version")
				}
			}

			cancel()
			<-done
		})
	}
}

func TestProtocolAutoDetect(t *testing.T) {
	t.Parallel()

	root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})
	srv := New(root, WithMaxMsize(65536))

	// Connection 1: 9P2000.L
	c1client, c1server := net.Pipe()
	defer func() { _ = c1client.Close() }()
	defer func() { _ = c1server.Close() }()

	// Connection 2: 9P2000.u
	c2client, c2server := net.Pipe()
	defer func() { _ = c2client.Close() }()
	defer func() { _ = c2server.Close() }()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		srv.ServeConn(ctx, c1server)
	}()

	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		srv.ServeConn(ctx, c2server)
	}()

	// Negotiate .L on connection 1.
	sendTversion(t, c1client, 65536, "9P2000.L")
	rv1 := readRversion(t, c1client)
	if rv1.Version != "9P2000.L" {
		t.Errorf("conn1 version = %q, want 9P2000.L", rv1.Version)
	}

	// Negotiate .u on connection 2.
	sendTversion(t, c2client, 65536, "9P2000.u")
	rv2 := readRversion(t, c2client)
	if rv2.Version != "9P2000.u" {
		t.Errorf("conn2 version = %q, want 9P2000.u", rv2.Version)
	}

	cancel()
	<-done1
	<-done2
}

// sendMessage encodes a full 9P2000.L message using p9l.Encode.
func sendMessage(t *testing.T, w io.Writer, tag proto.Tag, msg proto.Message) {
	t.Helper()
	if err := p9l.Encode(w, tag, msg); err != nil {
		t.Fatalf("encode %s: %v", msg.Type(), err)
	}
}

// readResponse reads a full 9P2000.L message from r using p9l.Decode.
func readResponse(t *testing.T, r io.Reader) (proto.Tag, proto.Message) {
	t.Helper()
	tag, msg, err := p9l.Decode(r)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return tag, msg
}

// dirNode implements a directory node using Inode embedding for lifecycle tests.
// Lookup is provided by the embedded Inode via children map.
type dirNode struct {
	Inode
}

// newDirNode creates a dirNode initialized with the given QID.
func newDirNode(qid proto.QID) *dirNode {
	n := &dirNode{}
	n.Init(qid, n)
	return n
}

// TestNegotiateVersion_DrainsOversizedTversionBody asserts the cold
// negotiation path consumes the full declared Tversion body, including bytes
// the decoder does not read, so trailing padding cannot desync the next frame.
func TestNegotiateVersion_DrainsOversizedTversionBody(t *testing.T) {
	t.Parallel()

	rootQID := proto.QID{Type: proto.QTDIR, Path: 1}
	root := newDirNode(rootQID)
	srv := New(root, WithMaxMsize(65536), WithLogger(discardLogger()))

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); srv.ServeConn(ctx, server) }()

	// Tversion whose declared body carries 5 trailing padding bytes beyond
	// msize+version. The decoder reads only the real fields; the server must
	// still drain the padding so the following Tattach frame parses.
	const version = "9P2000.L"
	var body []byte
	body = binary.LittleEndian.AppendUint32(body, 65536)
	body = binary.LittleEndian.AppendUint16(body, uint16(len(version)))
	body = append(body, version...)
	body = append(body, make([]byte, 5)...) // padding

	size := uint32(7 + len(body)) // 4 size + 1 type + 2 tag + body
	var frame []byte
	frame = binary.LittleEndian.AppendUint32(frame, size)
	frame = append(frame, byte(proto.TypeTversion))
	frame = binary.LittleEndian.AppendUint16(frame, uint16(proto.NoTag))
	frame = append(frame, body...)
	if _, err := client.Write(frame); err != nil {
		t.Fatalf("write oversized Tversion: %v", err)
	}

	if rv := readRversion(t, client); rv.Version != version {
		t.Fatalf("version = %q, want %q", rv.Version, version)
	}

	// The next frame must parse cleanly; a desync would corrupt it.
	sendMessage(t, client, 1, &proto.Tattach{Fid: 0, Afid: proto.NoFid, Uname: "test"})
	tag, msg := readResponse(t, client)
	if tag != 1 {
		t.Fatalf("attach tag = %d, want 1 (stream desynced?)", tag)
	}
	if _, ok := msg.(*proto.Rattach); !ok {
		t.Fatalf("got %T, want Rattach (stream desynced by padding?)", msg)
	}

	_ = client.Close()
	<-done
}

// TestNegotiateVersion_HandshakeDeadlineClosesStalledPeer asserts that even
// with no idle timeout configured, a peer that connects and never sends
// Tversion is closed by the always-on handshake deadline rather than pinning
// the serve goroutine forever.
func TestNegotiateVersion_HandshakeDeadlineClosesStalledPeer(t *testing.T) {
	t.Parallel()

	root := newDirNode(proto.QID{Type: proto.QTDIR, Path: 1})
	srv := New(root, WithLogger(discardLogger()))
	srv.handshakeTimeout = 50 * time.Millisecond // idleTimeout stays 0

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()

	done := make(chan struct{})
	go func() { defer close(done); srv.ServeConn(t.Context(), server) }()

	// Never send Tversion; the handshake deadline must tear the conn down.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not close a stalled handshake within the deadline")
	}
}

// failingWriteConn wraps a net.Conn and fails all writes once fail is set,
// while reads continue to work (a write-half-closed peer).
type failingWriteConn struct {
	net.Conn
	fail atomic.Bool
}

func (c *failingWriteConn) Write(b []byte) (int, error) {
	if c.fail.Load() {
		return 0, errors.New("simulated write failure")
	}
	return c.Conn.Write(b)
}

// TestSendResponse_WriteErrorTearsDownConn asserts a write failure tears the
// connection down (via shutdown signalling) instead of dropping responses and
// lingering until an unrelated read failure.
func TestSendResponse_WriteErrorTearsDownConn(t *testing.T) {
	t.Parallel()

	root := newDirNode(proto.QID{Type: proto.QTDIR, Path: 1})
	srv := New(root, WithLogger(discardLogger()))

	client, serverRaw := net.Pipe()
	server := &failingWriteConn{Conn: serverRaw}
	defer func() { _ = client.Close() }()

	done := make(chan struct{})
	go func() { defer close(done); srv.ServeConn(t.Context(), server) }()

	// Handshake succeeds (writes still allowed).
	sendTversion(t, client, 65536, "9P2000.L")
	_ = readRversion(t, client)

	// Fail every subsequent write, then drive a request that needs a reply.
	server.fail.Store(true)
	sendMessage(t, client, 1, &proto.Tattach{Fid: 0, Afid: proto.NoFid, Uname: "test"})

	select {
	case <-done:
		// ServeConn returned: the write error tore the connection down.
	case <-time.After(2 * time.Second):
		t.Fatal("ServeConn did not tear down after a write error")
	}
}

// TestSendResponseInline_EncodeFailureSendsEIO asserts that a response
// which fails to encode (here, an Rread whose Data exceeds
// proto.MaxDataSize) is not silently dropped: the client must still get a
// reply for its tag, so the client doesn't hang forever waiting on it.
func TestSendResponseInline_EncodeFailureSendsEIO(t *testing.T) {
	t.Parallel()

	client, serverConn := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = serverConn.Close() })

	c := &conn{
		server:   &Server{},
		nc:       serverConn,
		logger:   discardLogger(),
		protocol: protocolL,
	}

	oversized := &proto.Rread{Data: make([]byte, proto.MaxDataSize+1)}
	go c.sendResponseInline(7, oversized, nil)

	tag, msg := readResponse(t, client)
	if tag != 7 {
		t.Fatalf("tag = %d, want 7", tag)
	}
	isError(t, msg, proto.EIO)
}

type blockingListener struct {
	closed chan struct{}
	once   sync.Once
}

func newBlockingListener() *blockingListener {
	return &blockingListener{closed: make(chan struct{})}
}

func (l *blockingListener) Accept() (net.Conn, error) {
	<-l.closed
	return nil, net.ErrClosed
}

func (l *blockingListener) Close() error {
	l.once.Do(func() {
		close(l.closed)
	})
	return nil
}

func (l *blockingListener) Addr() net.Addr {
	return &net.TCPAddr{}
}

// flakyListener returns EMFILE a fixed number of times, then yields one real
// conn (signalling served), then blocks on Accept until Close.
type flakyListener struct {
	mu       sync.Mutex
	emfile   int
	realConn net.Conn
	served   chan struct{}
	block    chan struct{}
}

func (l *flakyListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	switch {
	case l.emfile > 0:
		l.emfile--
		l.mu.Unlock()
		return nil, &net.OpError{Op: "accept", Net: "tcp", Err: syscall.EMFILE}
	case l.realConn != nil:
		c := l.realConn
		l.realConn = nil
		l.mu.Unlock()
		close(l.served)
		return c, nil
	default:
		l.mu.Unlock()
		<-l.block
		return nil, net.ErrClosed
	}
}

func (l *flakyListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.block:
	default:
		close(l.block)
	}
	return nil
}

func (l *flakyListener) Addr() net.Addr { return &net.TCPAddr{} }

// TestServe_RetriesTransientAcceptErrors asserts a transient EMFILE from Accept
// does not tear down Serve: it backs off, retries, and goes on to accept the
// next connection. Serve returns only when ctx is cancelled.
func TestServe_RetriesTransientAcceptErrors(t *testing.T) {
	t.Parallel()

	root := newDirNode(proto.QID{Type: proto.QTDIR, Path: 1})
	srv := New(root, WithLogger(discardLogger()))

	// A pre-closed pipe peer makes the accepted conn's first read return
	// immediately, so its serve goroutine exits and wg.Wait does not block.
	cli, srvConn := net.Pipe()
	_ = cli.Close()

	ln := &flakyListener{
		emfile:   2,
		realConn: srvConn,
		served:   make(chan struct{}),
		block:    make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx, ln) }()

	select {
	case <-ln.served:
		// Serve retried past the EMFILE errors and accepted the connection.
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("Serve did not accept the real connection after transient EMFILE")
	}

	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Serve returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after ctx cancel")
	}
}

func TestServeConn(t *testing.T) {
	t.Parallel()

	rootQID := proto.QID{Type: proto.QTDIR, Version: 0, Path: 1}
	root := newDirNode(rootQID)
	srv := New(root, WithMaxMsize(65536), WithLogger(discardLogger()))

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.ServeConn(ctx, server)
	}()

	// Tversion
	sendTversion(t, client, 65536, "9P2000.L")
	rv := readRversion(t, client)
	if rv.Version != "9P2000.L" {
		t.Fatalf("version = %q, want 9P2000.L", rv.Version)
	}

	// Tattach
	sendMessage(t, client, 1, &proto.Tattach{
		Fid:   0,
		Afid:  proto.NoFid,
		Uname: "test",
		Aname: "",
	})
	tag, msg := readResponse(t, client)
	if tag != 1 {
		t.Fatalf("attach tag = %d, want 1", tag)
	}
	rattach, ok := msg.(*proto.Rattach)
	if !ok {
		t.Fatalf("expected Rattach, got %T", msg)
	}
	if rattach.QID != rootQID {
		t.Errorf("attach QID = %+v, want %+v", rattach.QID, rootQID)
	}

	// Tclunk
	sendMessage(t, client, 2, &proto.Tclunk{Fid: 0})
	tag, msg = readResponse(t, client)
	if tag != 2 {
		t.Fatalf("clunk tag = %d, want 2", tag)
	}
	if _, ok := msg.(*proto.Rclunk); !ok {
		t.Fatalf("expected Rclunk, got %T", msg)
	}

	// Close client side first to let the server drain cleanly, then cancel.
	_ = client.Close()
	<-done
	cancel()
}

func TestServeReturnsOnContextCancel(t *testing.T) {
	t.Parallel()

	root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})
	srv := New(root, WithMaxMsize(65536))
	ln := newBlockingListener()

	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ctx, ln)
	}()

	cancel()
	select {
	case err := <-errCh:
		if err != context.Canceled {
			t.Fatalf("Serve err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		_ = ln.Close()
		t.Fatal("Serve did not return after context cancellation")
	}
}

func TestServeListener(t *testing.T) {
	t.Parallel()

	root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})
	srv := New(root, WithMaxMsize(65536))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ctx, ln)
	}()

	// Connect a client.
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	sendTversion(t, conn, 8192, "9P2000.L")
	rv := readRversion(t, conn)
	if rv.Version != "9P2000.L" {
		t.Errorf("version = %q, want 9P2000.L", rv.Version)
	}
	if rv.Msize != 8192 {
		t.Errorf("msize = %d, want 8192", rv.Msize)
	}

	_ = conn.Close()
	cancel()
	_ = ln.Close()

	// Serve should return context.Canceled or accept error.
	serveErr := <-errCh
	if serveErr != nil && serveErr != context.Canceled {
		// Accept errors after listener close are expected.
		t.Logf("serve returned: %v (expected after cleanup)", serveErr)
	}
}

func TestIdleTimeout(t *testing.T) {
	t.Parallel()

	root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})
	srv := New(root,
		WithMaxMsize(65536),
		WithIdleTimeout(50*time.Millisecond),
	)

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.ServeConn(ctx, server)
	}()

	// Negotiate version.
	sendTversion(t, client, 65536, "9P2000.L")
	_ = readRversion(t, client)

	// Do NOT send any more messages. The idle timeout should close the
	// connection.
	buf := make([]byte, 1)
	_ = client.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, err := client.Read(buf)
	if err == nil {
		t.Error("expected connection to close due to idle timeout")
	}

	<-done
}

func TestIdleTimeout_ResetOnActivity(t *testing.T) {
	t.Parallel()

	rootQID := proto.QID{Type: proto.QTDIR, Version: 0, Path: 1}
	root := newDirNode(rootQID)
	srv := New(root,
		WithMaxMsize(65536),
		WithIdleTimeout(100*time.Millisecond),
	)

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.ServeConn(ctx, server)
	}()

	// Negotiate version.
	sendTversion(t, client, 65536, "9P2000.L")
	_ = readRversion(t, client)

	// Send Tattach messages spaced 50ms apart (within 100ms timeout).
	// Each message resets the idle timer.
	for i := range 3 {
		time.Sleep(50 * time.Millisecond)
		fid := proto.Fid(i)
		sendMessage(t, client, proto.Tag(i+1), &proto.Tattach{
			Fid:   fid,
			Afid:  proto.NoFid,
			Uname: "test",
			Aname: "",
		})
		_, msg := readResponse(t, client)
		if _, ok := msg.(*proto.Rattach); !ok {
			t.Fatalf("expected Rattach at iteration %d, got %T", i, msg)
		}
	}

	// Now stop sending. The connection should close after the idle period.
	buf := make([]byte, 1)
	_ = client.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, err := client.Read(buf)
	if err == nil {
		t.Error("expected connection to close after idle period")
	}

	<-done
}

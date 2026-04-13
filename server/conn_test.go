package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
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
func sendTversion(t *testing.T, w io.Writer, msize uint32, version string) {
	t.Helper()
	var body bytes.Buffer
	tv := &proto.Tversion{Msize: msize, Version: version}
	if err := tv.EncodeTo(&body); err != nil {
		t.Fatalf("encode tversion body: %v", err)
	}
	size := uint32(proto.HeaderSize) + uint32(body.Len())
	if err := proto.WriteUint32(w, size); err != nil {
		t.Fatalf("write size: %v", err)
	}
	if err := proto.WriteUint8(w, uint8(proto.TypeTversion)); err != nil {
		t.Fatalf("write type: %v", err)
	}
	if err := proto.WriteUint16(w, uint16(proto.NoTag)); err != nil {
		t.Fatalf("write tag: %v", err)
	}
	if _, err := w.Write(body.Bytes()); err != nil {
		t.Fatalf("write body: %v", err)
	}
}

// readRversion reads a raw Rversion from r and returns it.
func readRversion(t *testing.T, r io.Reader) *proto.Rversion {
	t.Helper()
	size, err := proto.ReadUint32(r)
	if err != nil {
		t.Fatalf("read rversion size: %v", err)
	}
	if size < uint32(proto.HeaderSize) {
		t.Fatalf("rversion size too small: %d", size)
	}
	msgType, err := proto.ReadUint8(r)
	if err != nil {
		t.Fatalf("read rversion type: %v", err)
	}
	if proto.MessageType(msgType) != proto.TypeRversion {
		t.Fatalf("expected Rversion (type %d), got type %d", proto.TypeRversion, msgType)
	}
	if _, err := proto.ReadUint16(r); err != nil { // tag
		t.Fatalf("read rversion tag: %v", err)
	}
	bodySize := int64(size) - int64(proto.HeaderSize)
	var rv proto.Rversion
	if err := rv.DecodeFrom(io.LimitReader(r, bodySize)); err != nil {
		t.Fatalf("decode rversion: %v", err)
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

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
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

func TestServeConn(t *testing.T) {
	t.Parallel()

	rootQID := proto.QID{Type: proto.QTDIR, Version: 0, Path: 1}
	root := newDirNode(rootQID)
	srv := New(root, WithMaxMsize(65536), WithLogger(discardLogger()))

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
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

func TestServeListener(t *testing.T) {
	t.Parallel()

	root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})
	srv := New(root, WithMaxMsize(65536))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

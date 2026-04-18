package client_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/server"
	"github.com/dotwaffle/ninep/server/memfs"
)

// discardLogger returns a slog.Logger that drops all records. Used by tests
// to keep the -v output readable when the server or client emits diagnostic
// logs during protocol exchanges.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// buildTestRoot constructs a small memfs tree used by the default
// integration test fixture. Callers that need a different shape pass their
// own server.Node to newClientServerPair.
//
// API note: server.QIDGenerator is instantiated via struct-literal
// (&server.QIDGenerator{}); there is NO server.NewQIDGenerator constructor.
// Verified at server/memfs/memfs_test.go:11 and server/memfs/builder.go:24.
// The zero-value QIDGenerator begins allocation at path=1 on first Next()
// call (see server/qid.go).
func buildTestRoot(tb testing.TB) server.Node {
	tb.Helper()
	gen := &server.QIDGenerator{}
	root := memfs.NewDir(gen).
		AddStaticFile("hello.txt", "hello world\n").
		AddStaticFile("empty.txt", "").
		AddFile("rw.bin", make([]byte, 0, 4096))
	return root
}

// newClientServerPair boots a server goroutine serving root over a net.Pipe
// and dials the client-side half. Returns the live *client.Conn and a
// cleanup func that Close()s the client, cancels the server's ctx, and
// waits for the server goroutine to exit.
//
// Plan 19-02 → Plan 19-03 bridging contract: while client/dial_stub.go is
// active, client.Dial returns (nil, nil). This helper detects cli == nil
// and calls tb.Skipf(...) so the test skips rather than fails. After Plan
// 19-03 Task 1 pre_flight deletes dial_stub.go and Plan 19-03 Task 2 ships
// the real Dial, the skip path is no longer taken and downstream tests
// exercise the real implementation.
//
// Mirrors server/walk_test.go:77's newConnPairTransport but wires in the
// real client.Dial on the client side instead of a hand-rolled Tversion
// exchange.
func newClientServerPair(tb testing.TB, root server.Node, clientOpts ...client.Option) (*client.Conn, func()) {
	tb.Helper()

	cliNC, srvNC := net.Pipe()

	srv := server.New(root,
		server.WithMaxMsize(65536),
		server.WithLogger(discardLogger()),
	)
	srvCtx, srvCancel := context.WithTimeout(tb.Context(), 30*time.Second)
	srvDone := make(chan struct{})
	go func() {
		defer close(srvDone)
		srv.ServeConn(srvCtx, srvNC)
	}()

	// Client-side: Dial runs Tversion inside. A tight ctor-time ctx (5s)
	// fails fast on negotiation bugs; the real Conn is expected to clear
	// the deadline once live (Plan 19-03 responsibility).
	dialCtx, dialCancel := context.WithTimeout(tb.Context(), 5*time.Second)
	defer dialCancel()

	defaultOpts := []client.Option{
		client.WithMsize(65536),
		client.WithLogger(discardLogger()),
	}
	opts := append(defaultOpts, clientOpts...)

	cli, err := client.Dial(dialCtx, cliNC, opts...)
	if err != nil {
		_ = cliNC.Close()
		srvCancel()
		<-srvDone
		tb.Fatalf("client.Dial: %v", err)
	}

	// Plan 19-02 → 19-03 bridging stub: Dial may return (nil, nil). Skip
	// rather than fail so CI stays green across the package while the stub
	// is active. Plan 19-03 Task 1 pre_flight removes client/dial_stub.go
	// and this branch becomes unreachable.
	if cli == nil {
		_ = cliNC.Close()
		srvCancel()
		<-srvDone
		tb.Skipf("client.Dial stub active; Plan 19-03 Task 1 pre_flight deletes client/dial_stub.go")
	}

	cleanup := func() {
		_ = cli.Close()
		srvCancel()
		_ = srvNC.Close()
		<-srvDone
	}
	return cli, cleanup
}

// TestPairHelper_Boots is a smoke test for newClientServerPair — it
// verifies the helper returns a non-nil Conn and a functioning cleanup.
// While the dial stub is active this test skips; after Plan 19-03 it
// exercises a real Conn boot.
func TestPairHelper_Boots(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	if cli == nil {
		t.Fatal("newClientServerPair returned nil Conn after skip-gate")
	}
}

// TestPairHelper_CleanupIdempotent verifies the cleanup function can be
// called more than once without panic or hang. This matters because many
// test patterns combine `defer cleanup()` with an explicit early
// `cleanup()` call in error paths.
func TestPairHelper_CleanupIdempotent(t *testing.T) {
	t.Parallel()
	_, cleanup := newClientServerPair(t, buildTestRoot(t))
	cleanup()
	cleanup() // must not panic or hang
}

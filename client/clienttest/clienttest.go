package clienttest

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

// defaultMsize is the msize applied to both the server (via
// server.WithMaxMsize) and client (via client.WithMsize) when neither
// WithMsize nor an explicit WithServerOpts/WithClientOpts overrides it.
// 65536 mirrors the precedent set by client/pair_test.go: it stays well
// under the client's 1 MiB default and bounds test allocations.
const defaultMsize uint32 = 65536

// config captures the resolved harness configuration.
//
// serverMsize and clientMsize use a zero-value "sentinel" convention:
// 0 means "no override supplied by the caller, use defaultMsize on that
// side". Options never write 0 explicitly — WithMsize always writes the
// caller's value to BOTH fields.
type config struct {
	serverOpts  []server.Option
	clientOpts  []client.Option
	serverMsize uint32
	clientMsize uint32
	parentCtx   context.Context
}

// newConfig returns a config with harness defaults: empty option slices,
// zero msize sentinels, and parentCtx pinned to context.Background().
// Options applied on top mutate the config in place.
func newConfig() *config {
	return &config{
		parentCtx: context.Background(),
	}
}

// Option configures a paired test harness created by [Pair] or
// [MemfsPair]. Options are applied in the order they are supplied;
// later options overwrite earlier ones for scalar fields and append for
// slice fields.
//
// The harness mirrors [net/http/httptest] in shape and ergonomics.
type Option func(*config)

// WithServerOpts adds server-side options that are forwarded verbatim to
// [server.New]. Use this to configure middleware, loggers, or any
// server.Option that is not shadowed by a harness-level helper such as
// [WithMsize].
//
// Repeated calls append in order. Server options applied here run after
// the harness's default server options, so caller overrides win.
func WithServerOpts(opts ...server.Option) Option {
	return func(c *config) { c.serverOpts = append(c.serverOpts, opts...) }
}

// WithClientOpts adds client-side options that are forwarded verbatim
// to [client.Dial]. Use this for per-test logger, per-test msize asymmetry
// (paired with [WithServerOpts]), or any client.Option that is not
// shadowed by a harness-level helper such as [WithMsize].
//
// Repeated calls append in order. Client options applied here run after
// the harness's default client options, so caller overrides win.
func WithClientOpts(opts ...client.Option) Option {
	return func(c *config) { c.clientOpts = append(c.clientOpts, opts...) }
}

// WithMsize sets the proposed maximum message size on BOTH the server
// (via [server.WithMaxMsize]) and the client (via [client.WithMsize]).
// This is a convenience shorthand for the common symmetric case.
//
// For asymmetric tuning — e.g. a server cap smaller than the client
// proposal to exercise the negotiation floor — bypass WithMsize and use
// [WithServerOpts] + [WithClientOpts] directly:
//
//	clienttest.Pair(t, root,
//	    clienttest.WithServerOpts(server.WithMaxMsize(4096)),
//	    clienttest.WithClientOpts(client.WithMsize(65536)),
//	)
func WithMsize(n uint32) Option {
	return func(c *config) {
		c.serverMsize = n
		c.clientMsize = n
	}
}

// WithCtx sets the parent context used to derive the server's serve ctx
// and the client's Dial ctx. Cancelling the parent unblocks the server
// goroutine spawned by [Pair]; [tb.Cleanup] additionally cancels it at
// test end.
//
// Passing nil is silently coerced to [context.Background] — the harness
// never panics on a nil ctx, matching the defensive posture of other
// standard-library test helpers.
func WithCtx(ctx context.Context) Option {
	return func(c *config) {
		if ctx == nil {
			ctx = context.Background()
		}
		c.parentCtx = ctx
	}
}

// discardLogger returns a slog.Logger that drops all records. Used as
// the harness default so -v test output stays readable when the server
// or client emits diagnostic logs during protocol exchanges. Callers who
// want to see logs supply their own via WithServerOpts / WithClientOpts.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Pair boots a server goroutine serving root over a net.Pipe and dials
// the client-side half. Returns the live *server.Server and *client.Conn
// so tests can drive from either side.
//
// Teardown is registered via tb.Cleanup: the client is Close()d, the
// server ctx is cancelled, the client net.Pipe half is closed, and the
// server goroutine is drained. Callers do NOT need to track a cleanup
// closure themselves.
//
// The harness applies sensible defaults:
//   - msize = 65536 on both sides (bounds test allocations)
//   - discard logger on both sides (keeps -v output readable)
//   - 30 s server ctx deadline (fail-fast on serve-loop hangs)
//   - 5 s dial ctx deadline (fail-fast on Tversion negotiation hangs)
//
// Caller-supplied options (via WithServerOpts / WithClientOpts) are
// appended after the harness defaults, so caller overrides always win.
//
// A nil root is a programming error and is surfaced by [server.New],
// not by Pair — the harness does not second-guess server.New's contract.
func Pair(tb testing.TB, root server.Node, opts ...Option) (*server.Server, *client.Conn) {
	tb.Helper()

	cfg := newConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.parentCtx == nil {
		cfg.parentCtx = context.Background()
	}

	cliNC, srvNC := net.Pipe()

	srvMsize := cfg.serverMsize
	if srvMsize == 0 {
		srvMsize = defaultMsize
	}
	serverOpts := []server.Option{
		server.WithMaxMsize(srvMsize),
		server.WithLogger(discardLogger()),
	}
	serverOpts = append(serverOpts, cfg.serverOpts...)

	srv := server.New(root, serverOpts...)

	srvCtx, srvCancel := context.WithTimeout(cfg.parentCtx, 30*time.Second)
	srvDone := make(chan struct{})
	go func() {
		defer close(srvDone)
		srv.ServeConn(srvCtx, srvNC)
	}()

	cliMsize := cfg.clientMsize
	if cliMsize == 0 {
		cliMsize = defaultMsize
	}
	clientOpts := []client.Option{
		client.WithMsize(cliMsize),
		client.WithLogger(discardLogger()),
	}
	clientOpts = append(clientOpts, cfg.clientOpts...)

	dialCtx, dialCancel := context.WithTimeout(cfg.parentCtx, 5*time.Second)
	defer dialCancel()

	cli, err := client.Dial(dialCtx, cliNC, clientOpts...)
	if err != nil {
		_ = cliNC.Close()
		srvCancel()
		<-srvDone
		tb.Fatalf("clienttest.Pair: client.Dial: %v", err)
		return nil, nil // unreachable; tb.Fatalf aborts
	}
	// Post-19-03: Dial never returns (nil, nil). If the contract is
	// violated, fail loudly rather than silently skip — tests must
	// surface API regressions.
	if cli == nil {
		_ = cliNC.Close()
		srvCancel()
		<-srvDone
		tb.Fatalf("clienttest.Pair: client.Dial returned nil Conn with nil error (API contract violation)")
		return nil, nil // unreachable
	}

	tb.Cleanup(func() {
		_ = cli.Close()
		srvCancel()
		_ = srvNC.Close()
		<-srvDone
	})

	return srv, cli
}

// MemfsPair is sugar for the common case where the server root is a
// memfs tree. It allocates a fresh *server.QIDGenerator, constructs an
// empty *memfs.MemDir as the root, invokes build(root) so the caller can
// populate it with the memfs fluent builder API, and then pairs via
// [Pair].
//
// A nil build callback is treated as a no-op, yielding a paired server
// with an empty-but-valid root. This matches test-harness ergonomics:
// callers that just want a boot-smoke harness should not be forced to
// pass an empty closure.
//
// Example:
//
//	srv, cli := clienttest.MemfsPair(t, func(root *memfs.MemDir) {
//	    root.AddStaticFile("hello.txt", "hello world\n")
//	})
//	_ = srv
//	_, _ = cli.Attach(t.Context(), "example", "")
func MemfsPair(tb testing.TB, build func(root *memfs.MemDir), opts ...Option) (*server.Server, *client.Conn) {
	tb.Helper()
	gen := &server.QIDGenerator{}
	root := memfs.NewDir(gen)
	if build != nil {
		build(root)
	}
	return Pair(tb, root, opts...)
}

package clienttest

import (
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"runtime"
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

// defaultServeTimeout bounds the server goroutine's serve ctx so a
// hung handler fails fast instead of stalling the test binary until the
// outer testing framework kills it. Overridable via [WithServeTimeout];
// automatically shortened when [testing.TB.Deadline] reports one.
const defaultServeTimeout = 30 * time.Second

// defaultDialTimeout bounds the client-side Dial ctx so a stuck
// Tversion negotiation fails fast with a dial-timeout error rather than
// silently hanging. Overridable via [WithDialTimeout].
const defaultDialTimeout = 5 * time.Second

// deadlineSafetyMargin is subtracted from [testing.TB.Deadline] when
// deriving the serve timeout so tb.Cleanup has time to run before the
// testing framework force-kills the test. Matches the spirit of
// net/http/httptest.Server.Close (give teardown a breath).
const deadlineSafetyMargin = 100 * time.Millisecond

// config captures the resolved harness configuration.
//
// serverMsize, clientMsize, serveTimeout, and dialTimeout use a
// zero-value "sentinel" convention: 0 means "no override supplied by the
// caller, use the harness default". Options never write 0 explicitly —
// WithMsize always writes the caller's value to BOTH msize fields, and
// WithServeTimeout / WithDialTimeout only write their respective field.
type config struct {
	serverOpts   []server.Option
	clientOpts   []client.Option
	serverMsize  uint32
	clientMsize  uint32
	serveTimeout time.Duration
	dialTimeout  time.Duration
	parentCtx    context.Context
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

// WithServeTimeout overrides the default server-side serve ctx timeout
// (30 s). Pass a larger value for slow-CI / -race / breakpoint-debugging
// sessions, or pass 0 to fall back to the harness default.
//
// When the surrounding [testing.TB.Deadline] is closer than the supplied
// duration, Pair still honours it (minus a small safety margin) so
// tb.Cleanup runs before the testing framework force-kills the test.
func WithServeTimeout(d time.Duration) Option {
	return func(c *config) { c.serveTimeout = d }
}

// WithDialTimeout overrides the default client Dial ctx timeout (5 s).
// Useful when the underlying transport or Tversion negotiation path
// runs under -race / -cpu=1 and the default would false-positive. Pass
// 0 to fall back to the harness default.
func WithDialTimeout(d time.Duration) Option {
	return func(c *config) { c.dialTimeout = d }
}

// WithCtx sets the parent context used to derive the server's serve ctx
// and the client's Dial ctx. Cancelling the parent unblocks the server
// goroutine spawned by [Pair].
//
// tb.Cleanup cancels the DERIVED server ctx (not the caller-supplied
// parent), closes the client, closes the server-side pipe half, and
// drains the server goroutine. The parent ctx is the caller's to manage;
// the harness never propagates cancellation upward into it.
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
// Teardown is registered via tb.Cleanup: the client is Close()d (which
// closes its pipe half), the derived server ctx is cancelled, the
// server-side net.Pipe half is closed, and the server goroutine is
// drained. Callers do NOT need to track a cleanup closure themselves.
//
// The harness applies sensible defaults:
//   - msize = 65536 on both sides (bounds test allocations)
//   - discard logger on both sides (keeps -v output readable)
//   - 30 s server ctx timeout (fail-fast on serve-loop hangs)
//   - 5 s dial ctx timeout (fail-fast on Tversion negotiation hangs)
//
// When [testing.TB.Deadline] reports a deadline closer than the serve
// timeout, Pair shortens the serve ctx to fire just before that deadline
// so tb.Cleanup can run before the testing framework force-kills the
// test.
//
// Caller-supplied options (via WithServerOpts / WithClientOpts) are
// appended after the harness defaults, so caller overrides always win.
// Timeout overrides via [WithServeTimeout] / [WithDialTimeout] replace
// the defaults outright.
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
	return finalizePair(tb, cfg, root, cliNC, srvNC, "clienttest.Pair")
}

// UnixPair is the net.UnixConn analogue of [Pair]. It boots a server
// goroutine serving root over a unix-domain socket in tb.TempDir() and
// dials the client-side half. Returns the live *server.Server and
// *client.Conn so tests can drive from either side.
//
// UnixPair skips the test on Windows (runtime.GOOS == "windows") since
// unix-domain sockets are not broadly supported there.
//
// Callers needing the writev fast path for bench validation (SC-4 mirror
// benches) use UnixPair; correctness-only tests should use [Pair] for
// cross-platform coverage and lower overhead.
//
// The harness applies the same defaults and Option semantics as [Pair];
// see Pair's godoc for the full contract. Teardown is registered via
// tb.Cleanup: the listener is closed AFTER the client+server drain so
// teardown order is LIFO-safe.
func UnixPair(tb testing.TB, root server.Node, opts ...Option) (*server.Server, *client.Conn) {
	tb.Helper()
	if runtime.GOOS == "windows" {
		tb.Skipf("clienttest.UnixPair: unix transport not supported on windows")
	}

	cfg := newConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.parentCtx == nil {
		cfg.parentCtx = context.Background()
	}

	sockPath := filepath.Join(tb.TempDir(), "ninep.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		tb.Fatalf("clienttest.UnixPair: listen unix: %v", err)
		return nil, nil
	}
	// Register listener close BEFORE finalizePair registers its cleanups:
	// Cleanup runs LIFO, so the listener closes AFTER the client+server
	// drain, avoiding spurious read/accept errors during teardown.
	tb.Cleanup(func() { _ = ln.Close() })

	// Accept runs concurrently with Dial. The accept goroutine signals
	// through `accepted` so the main goroutine can surface accept errors
	// with test-appropriate diagnostics rather than blocking.
	//
	// WR-02: defense against an Accept that completes after the main
	// goroutine has already moved on (e.g. timer arm won the select
	// race). The non-blocking send + close-on-default means a late
	// successful Accept doesn't park forever AND doesn't leak the
	// server-side conn — the goroutine closes it immediately if the
	// recipient is gone. ln.Close in tb.Cleanup unblocks a still-blocked
	// Accept with net.ErrClosed.
	type acceptResult struct {
		nc  net.Conn
		err error
	}
	accepted := make(chan acceptResult, 1)
	go func() {
		c, err := ln.Accept()
		select {
		case accepted <- acceptResult{nc: c, err: err}:
		default:
			if c != nil {
				_ = c.Close()
			}
		}
	}()

	dialTimeout := cfg.dialTimeout
	if dialTimeout == 0 {
		dialTimeout = defaultDialTimeout
	}
	cliNC, err := net.DialTimeout("unix", sockPath, dialTimeout)
	if err != nil {
		tb.Fatalf("clienttest.UnixPair: dial unix: %v", err)
		return nil, nil
	}

	// WR-02: use time.NewTimer/Stop instead of time.After so the timer
	// goroutine is reclaimed on the success path (time.After always
	// leaks a goroutine until its duration elapses). Drain `accepted`
	// non-blockingly in the timeout arm so a late accept's conn is
	// closed rather than leaked — Go's uniform-random select can pick
	// the timer arm even after accepted is ready, and silently leaking
	// the server-side conn until process exit is wrong.
	timer := time.NewTimer(dialTimeout)
	defer timer.Stop()
	select {
	case ar := <-accepted:
		if ar.err != nil {
			_ = cliNC.Close()
			tb.Fatalf("clienttest.UnixPair: accept: %v", ar.err)
			return nil, nil
		}
		return finalizePair(tb, cfg, root, cliNC, ar.nc, "clienttest.UnixPair")
	case <-timer.C:
		_ = cliNC.Close()
		// Drain a late-arriving accept result so its conn is closed
		// rather than leaked. Non-blocking — if accept hasn't returned
		// yet, ln.Close in tb.Cleanup will surface net.ErrClosed and
		// the accept goroutine's default arm closes any partial conn.
		select {
		case ar := <-accepted:
			if ar.err == nil && ar.nc != nil {
				_ = ar.nc.Close()
			}
		default:
		}
		tb.Fatalf("clienttest.UnixPair: accept timed out after %s", dialTimeout)
		return nil, nil
	}
}

// finalizePair wires a server + client over already-constructed net.Conn
// halves. Shared between [Pair] (net.Pipe) and [UnixPair] (unix-domain
// sockets) to dedupe option application, server spawn, Dial, and the
// tb.Cleanup ordering.
//
// cfg is the fully-applied option config; root is the server-side Node;
// cliNC / srvNC are the client-side and server-side net.Conn halves,
// both already open and paired. caller is the diagnostic prefix used in
// tb.Fatalf messages ("clienttest.Pair" / "clienttest.UnixPair").
//
// On success, returns the live *server.Server and *client.Conn and
// registers tb.Cleanup to tear both down. On failure, fails via
// tb.Fatalf and never returns.
func finalizePair(tb testing.TB, cfg *config, root server.Node, cliNC, srvNC net.Conn, caller string) (*server.Server, *client.Conn) {
	tb.Helper()

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

	serveTimeout := cfg.serveTimeout
	if serveTimeout == 0 {
		serveTimeout = defaultServeTimeout
	}
	// Shorten the serve timeout when the testing framework itself has a
	// nearer deadline, leaving a small margin so tb.Cleanup runs before
	// the outer deadline fires. Deadline is exposed only on *testing.T
	// (not on the TB interface, and not on *testing.B / *testing.F), so
	// narrow via type assertion.
	type deadliner interface {
		Deadline() (time.Time, bool)
	}
	if dl, ok := tb.(deadliner); ok {
		if d, have := dl.Deadline(); have {
			if remaining := time.Until(d) - deadlineSafetyMargin; remaining > 0 && remaining < serveTimeout {
				serveTimeout = remaining
			}
		}
	}

	srvCtx, srvCancel := context.WithTimeout(cfg.parentCtx, serveTimeout)
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

	dialTimeout := cfg.dialTimeout
	if dialTimeout == 0 {
		dialTimeout = defaultDialTimeout
	}
	dialCtx, dialCancel := context.WithTimeout(cfg.parentCtx, dialTimeout)
	defer dialCancel()

	cli, err := client.Dial(dialCtx, cliNC, clientOpts...)
	if err != nil {
		_ = cliNC.Close()
		srvCancel()
		<-srvDone
		tb.Fatalf("%s: client.Dial: %v", caller, err)
		return nil, nil // unreachable; tb.Fatalf aborts
	}
	// Post-19-03: Dial never returns (nil, nil). If the contract is
	// violated, fail loudly rather than silently skip — tests must
	// surface API regressions.
	if cli == nil {
		_ = cliNC.Close()
		srvCancel()
		<-srvDone
		tb.Fatalf("%s: client.Dial returned nil Conn with nil error (API contract violation)", caller)
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

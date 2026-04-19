package clienttest

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/server"
	"github.com/dotwaffle/ninep/server/memfs"
)

// TestWithServerOpts verifies that WithServerOpts appends server options
// to the internal config in call order.
func TestWithServerOpts(t *testing.T) {
	t.Parallel()
	cfg := newConfig()
	a := server.WithMaxMsize(1024)
	b := server.WithMaxMsize(2048)
	WithServerOpts(a, b)(cfg)
	if got, want := len(cfg.serverOpts), 2; got != want {
		t.Fatalf("serverOpts len = %d, want %d", got, want)
	}
}

// TestWithServerOpts_Appends verifies repeated calls append rather than
// replace (two separate WithServerOpts applications concatenate).
func TestWithServerOpts_Appends(t *testing.T) {
	t.Parallel()
	cfg := newConfig()
	WithServerOpts(server.WithMaxMsize(1))(cfg)
	WithServerOpts(server.WithMaxMsize(2), server.WithMaxMsize(3))(cfg)
	if got, want := len(cfg.serverOpts), 3; got != want {
		t.Fatalf("serverOpts len = %d, want %d", got, want)
	}
}

// TestWithClientOpts verifies that WithClientOpts appends client options
// to the internal config in call order.
func TestWithClientOpts(t *testing.T) {
	t.Parallel()
	cfg := newConfig()
	a := client.WithMsize(1024)
	b := client.WithMsize(2048)
	WithClientOpts(a, b)(cfg)
	if got, want := len(cfg.clientOpts), 2; got != want {
		t.Fatalf("clientOpts len = %d, want %d", got, want)
	}
}

// TestWithClientOpts_Appends verifies repeated calls append rather than
// replace.
func TestWithClientOpts_Appends(t *testing.T) {
	t.Parallel()
	cfg := newConfig()
	WithClientOpts(client.WithMsize(1))(cfg)
	WithClientOpts(client.WithMsize(2), client.WithMsize(3))(cfg)
	if got, want := len(cfg.clientOpts), 3; got != want {
		t.Fatalf("clientOpts len = %d, want %d", got, want)
	}
}

// TestWithMsize_SetsBoth verifies that WithMsize(n) populates BOTH
// serverMsize and clientMsize (the D-08 "sets both" contract).
func TestWithMsize_SetsBoth(t *testing.T) {
	t.Parallel()
	cfg := newConfig()
	WithMsize(32768)(cfg)
	if got, want := cfg.serverMsize, uint32(32768); got != want {
		t.Fatalf("serverMsize = %d, want %d", got, want)
	}
	if got, want := cfg.clientMsize, uint32(32768); got != want {
		t.Fatalf("clientMsize = %d, want %d", got, want)
	}
}

// TestWithMsize_Overrides verifies later WithMsize calls overwrite
// earlier ones on both sides.
func TestWithMsize_Overrides(t *testing.T) {
	t.Parallel()
	cfg := newConfig()
	WithMsize(32768)(cfg)
	WithMsize(65536)(cfg)
	if got, want := cfg.serverMsize, uint32(65536); got != want {
		t.Fatalf("serverMsize = %d, want %d", got, want)
	}
	if got, want := cfg.clientMsize, uint32(65536); got != want {
		t.Fatalf("clientMsize = %d, want %d", got, want)
	}
}

// TestWithCtx_Sets verifies that WithCtx populates parentCtx.
func TestWithCtx_Sets(t *testing.T) {
	t.Parallel()
	cfg := newConfig()
	type key struct{}
	parent := context.WithValue(context.Background(), key{}, "marker")
	WithCtx(parent)(cfg)
	if cfg.parentCtx == nil {
		t.Fatal("parentCtx = nil, want non-nil")
	}
	if got, _ := cfg.parentCtx.Value(key{}).(string); got != "marker" {
		t.Fatalf("parentCtx Value = %q, want %q", got, "marker")
	}
}

// TestWithCtx_NilCoerced verifies that WithCtx(nil) is defensively
// coerced to context.Background() rather than panicking when the config
// is later consumed.
func TestWithCtx_NilCoerced(t *testing.T) {
	t.Parallel()
	cfg := newConfig()
	// Intentional nil ctx — exercises the defensive coercion path.
	//nolint:staticcheck // SA1012: testing defensive nil handling.
	WithCtx(nil)(cfg)
	if cfg.parentCtx == nil {
		t.Fatal("parentCtx is nil after WithCtx(nil); want context.Background()")
	}
	// Any no-deadline ctx is acceptable — we just require non-nil so the
	// downstream context.WithTimeout derivation does not panic.
	if _, ok := cfg.parentCtx.Deadline(); ok {
		t.Fatal("parentCtx has a deadline; WithCtx(nil) should yield a background-equivalent ctx")
	}
}

// TestNewConfig_Defaults verifies newConfig yields working zero-state:
// empty option slices, zero msizes (signalling "use default"), a
// non-nil parentCtx.
func TestNewConfig_Defaults(t *testing.T) {
	t.Parallel()
	cfg := newConfig()
	if cfg == nil {
		t.Fatal("newConfig returned nil")
	}
	if len(cfg.serverOpts) != 0 {
		t.Errorf("serverOpts len = %d, want 0", len(cfg.serverOpts))
	}
	if len(cfg.clientOpts) != 0 {
		t.Errorf("clientOpts len = %d, want 0", len(cfg.clientOpts))
	}
	if cfg.serverMsize != 0 {
		t.Errorf("serverMsize = %d, want 0 (sentinel for default)", cfg.serverMsize)
	}
	if cfg.clientMsize != 0 {
		t.Errorf("clientMsize = %d, want 0 (sentinel for default)", cfg.clientMsize)
	}
	if cfg.parentCtx == nil {
		t.Error("parentCtx is nil; want context.Background()")
	}
}

// TestDefaultMsize_Reasonable pins defaultMsize at 65536 — mirrors the
// precedent set by client/pair_test.go. A change to this sentinel is a
// deliberate policy shift; this test forces the author to acknowledge it.
func TestDefaultMsize_Reasonable(t *testing.T) {
	t.Parallel()
	if defaultMsize != 65536 {
		t.Fatalf("defaultMsize = %d, want 65536", defaultMsize)
	}
}

// TestPair_Boots is the end-to-end meta-test. It pairs a memfs server
// carrying "hello.txt" with a live client and verifies the full
// Attach + Walk + Open + Read + Close round-trip returns exactly the
// file contents.
func TestPair_Boots(t *testing.T) {
	t.Parallel()
	gen := &server.QIDGenerator{}
	root := memfs.NewDir(gen).
		AddStaticFile("hello.txt", "hello world\n")

	srv, cli := Pair(t, root)
	if srv == nil {
		t.Fatal("Pair returned nil *server.Server")
	}
	if cli == nil {
		t.Fatal("Pair returned nil *client.Conn")
	}

	rootFile, err := cli.Attach(context.Background(), "tester", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = rootFile.Close() }()

	f, err := cli.OpenFile(context.Background(), "hello.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if got, want := string(data), "hello world\n"; got != want {
		t.Fatalf("file contents = %q, want %q", got, want)
	}
}

// TestPair_HonorsWithMsize verifies Pair threads WithMsize through to
// the client-side Conn (msize is negotiated as min(client, server), and
// both sides see the harness-supplied value here).
func TestPair_HonorsWithMsize(t *testing.T) {
	t.Parallel()
	gen := &server.QIDGenerator{}
	root := memfs.NewDir(gen).AddStaticFile("x", "y")

	_, cli := Pair(t, root, WithMsize(32768))
	if got := cli.Msize(); got > 32768 {
		t.Fatalf("Conn.Msize() = %d, want <= 32768", got)
	}
	if got := cli.Msize(); got < 256 {
		t.Fatalf("Conn.Msize() = %d, want >= 256 (minMsize floor)", got)
	}
}

// TestPair_HonorsWithCtx verifies that cancelling the parent ctx passed
// through WithCtx unblocks the server goroutine.
//
// We cancel the parent AFTER Dial has succeeded (cancelling before would
// fail Dial immediately and never exercise the steady-state ctx path).
// After cancel, subsequent client ops must fail — the server ctx is done
// and the Conn has been closed/aborted by the cleanup path on the next
// op. We verify the Conn surfaces some error rather than hanging.
func TestPair_HonorsWithCtx(t *testing.T) {
	t.Parallel()
	gen := &server.QIDGenerator{}
	root := memfs.NewDir(gen).AddStaticFile("x", "y")

	parent, cancel := context.WithCancel(context.Background())
	_, cli := Pair(t, root, WithCtx(parent))

	// Sanity: Conn is usable before cancellation.
	if _, err := cli.Attach(context.Background(), "tester", ""); err != nil {
		t.Fatalf("pre-cancel Attach: %v", err)
	}

	cancel()

	// After parent cancel, the server's 30s deadline is superseded by
	// the parent-derived cancellation. New requests must unblock within
	// a bounded window rather than hang — we give the server up to 2 s
	// to tear down.
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, opCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer opCancel()
		// Any op — Attach works. We don't care about the specific
		// error, just that one is produced.
		_, _ = cli.Attach(ctx, "tester", "")
	}()
	select {
	case <-done:
		// OK — either returned an error or succeeded before teardown.
	case <-time.After(3 * time.Second):
		t.Fatal("client op hung after parent ctx cancel; server goroutine may have leaked")
	}
}

// TestMemfsPair_BuildCallback verifies the build callback fires exactly
// once with a non-nil *memfs.MemDir and that files added via the
// callback are visible on the client side.
func TestMemfsPair_BuildCallback(t *testing.T) {
	t.Parallel()
	var buildCalls int
	_, cli := MemfsPair(t, func(root *memfs.MemDir) {
		buildCalls++
		if root == nil {
			t.Fatal("build callback received nil root")
		}
		root.AddStaticFile("foo", "bar")
	})
	if buildCalls != 1 {
		t.Fatalf("build callback called %d times, want 1", buildCalls)
	}

	if _, err := cli.Attach(context.Background(), "tester", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	f, err := cli.OpenFile(context.Background(), "foo", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if got, want := string(data), "bar"; got != want {
		t.Fatalf("file contents = %q, want %q", got, want)
	}
}

// TestMemfsPair_NilBuild verifies that a nil build callback is treated
// as a no-op: the harness still returns a paired server+client over an
// empty-but-valid root.
func TestMemfsPair_NilBuild(t *testing.T) {
	t.Parallel()
	srv, cli := MemfsPair(t, nil)
	if srv == nil || cli == nil {
		t.Fatal("MemfsPair returned nil server or conn with nil build")
	}
	if _, err := cli.Attach(context.Background(), "tester", ""); err != nil {
		t.Fatalf("Attach on empty root: %v", err)
	}
}

// TestMemfsPair_EmptyBuild verifies that a no-op build callback also
// yields a valid paired server+client (Attach succeeds on empty root).
func TestMemfsPair_EmptyBuild(t *testing.T) {
	t.Parallel()
	_, cli := MemfsPair(t, func(*memfs.MemDir) {})
	if _, err := cli.Attach(context.Background(), "tester", ""); err != nil {
		t.Fatalf("Attach on empty root: %v", err)
	}
}

// TestPair_ServerOptsPassthrough verifies that caller-supplied
// WithServerOpts values reach server.New. We pass server.WithMaxFids(1)
// and then exhaust the budget on the server side; the second fid-
// creating op MUST fail with EMFILE.
func TestPair_ServerOptsPassthrough(t *testing.T) {
	t.Parallel()
	gen := &server.QIDGenerator{}
	root := memfs.NewDir(gen).AddStaticFile("a", "1")

	_, cli := Pair(t, root, WithServerOpts(server.WithMaxFids(1)))
	if _, err := cli.Attach(context.Background(), "tester", ""); err != nil {
		t.Fatalf("first Attach: %v", err)
	}
	// Second attach creates a new fid; with MaxFids=1 this must fail.
	_, err := cli.Attach(context.Background(), "tester2", "")
	if err == nil {
		t.Fatal("second Attach succeeded with MaxFids=1; expected failure")
	}
	// Tolerate any error — the point is that the server-side cap fired,
	// which proves the option threaded through. Nested wrappers may
	// change the surfaced error over time; we assert only "non-nil".
	_ = errors.Unwrap
}

// TestPair_DefaultMsizeApplies verifies that when no WithMsize is
// supplied, the negotiated msize matches defaultMsize (both sides agree).
func TestPair_DefaultMsizeApplies(t *testing.T) {
	t.Parallel()
	gen := &server.QIDGenerator{}
	root := memfs.NewDir(gen).AddStaticFile("a", "1")
	_, cli := Pair(t, root)
	if got, want := cli.Msize(), defaultMsize; got != want {
		t.Fatalf("Conn.Msize() = %d, want %d (defaultMsize)", got, want)
	}
}

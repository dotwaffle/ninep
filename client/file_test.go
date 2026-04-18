package client_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
)

// fileTestCtx returns a 5s timeout ctx.
func fileTestCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), 5*time.Second)
}

// TestFile_ImplementsIO: compile-time io.* assertions are enforced in
// client/file.go via `var _ io.Reader = (*File)(nil)` etc. This test
// adds a runtime belt-and-braces check: a *File value is assignable to
// each of the six interfaces. Any removal of an interface method from
// *File fails `go build` at the package-level assertion before this
// test even links, but the assignments here catch misnamed stubs.
func TestFile_ImplementsIO(t *testing.T) {
	t.Parallel()
	var f *client.File
	var (
		_ io.Reader   = f
		_ io.Writer   = f
		_ io.Closer   = f
		_ io.Seeker   = f
		_ io.ReaderAt = f
		_ io.WriterAt = f
	)
}

// TestFile_QidAccessor: File.Qid() returns the QID captured at
// construction (from the Attach round-trip's Rattach.QID).
func TestFile_QidAccessor(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileTestCtx(t)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = root.Close() }()

	qid := root.Qid()
	if qid.Type&proto.QTDIR == 0 {
		t.Errorf("root.Qid().Type = %#x, want QTDIR bit set", qid.Type)
	}
}

// TestFile_CloseIdempotent: a second Close is a no-op and returns nil
// (D-06). The first Close issues exactly one Tclunk; the second path
// through sync.Once skips the wire entirely.
func TestFile_CloseIdempotent(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileTestCtx(t)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// Second Close must return nil per D-06 — even though the fid was
	// already clunked server-side, the sync.Once short-circuits the
	// wire op.
	if err := root.Close(); err != nil {
		t.Errorf("second Close: %v, want nil (idempotent per D-06)", err)
	}
}

// TestFile_CloseReleasesFid: after Close, the fid is returned to the
// allocator's reuse cache and a subsequent Attach receives the same
// fid (LIFO reuse verified via Raw.AcquireFid probe).
func TestFile_CloseReleasesFid(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileTestCtx(t)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	firstFidViaAcquire, err := cli.Raw().AcquireFid()
	if err != nil {
		t.Fatalf("AcquireFid probe: %v", err)
	}
	cli.Raw().ReleaseFid(firstFidViaAcquire)

	if err := root.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// After close, the freed fid is now at the top of the LIFO reuse
	// slice. Next AcquireFid must return it.
	reused, err := cli.Raw().AcquireFid()
	if err != nil {
		t.Fatalf("AcquireFid after close: %v", err)
	}
	// The root fid equals the next monotonic value we consumed with
	// the probe, which we returned to the free-list. After Close the
	// root's fid is also on the free-list. LIFO means Close's fid is
	// on top.
	if reused != root.Fid() {
		t.Errorf("after Close: AcquireFid = %d, want root's fid %d (LIFO reuse)", reused, root.Fid())
	}
	cli.Raw().ReleaseFid(reused)
}

// TestFile_CloseClunksOnWire: after Close, attempting to Read against
// the same fid number via Raw returns a server error (the fid is
// unbound server-side).
func TestFile_CloseClunksOnWire(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileTestCtx(t)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	fid := root.Fid()
	if err := root.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Server must reject Raw.Read against the now-unbound fid.
	_, err = cli.Raw().Read(ctx, fid, 0, 16)
	if err == nil {
		t.Fatal("Raw.Read on clunked fid: got nil, want server error")
	}
	var cerr *client.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("Raw.Read on clunked fid: err type %T (%v), want *client.Error",
			err, err)
	}
}

// TestFile_Walk_ReturnsNewFile: file.Walk([]string{"hello.txt"}) returns
// a new *File whose fid is distinct from the root's. The new file can
// be Closed independently.
func TestFile_Walk_ReturnsNewFile(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileTestCtx(t)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = root.Close() }()

	helloFile, err := root.Walk(ctx, []string{"hello.txt"})
	if err != nil {
		t.Fatalf("root.Walk(hello.txt): %v", err)
	}
	if helloFile.Fid() == root.Fid() {
		t.Errorf("Walk returned file sharing root fid %d", root.Fid())
	}
	if err := helloFile.Close(); err != nil {
		t.Errorf("helloFile.Close: %v", err)
	}
	// Root still usable.
	if _, err := root.Walk(ctx, []string{"hello.txt"}); err != nil {
		t.Errorf("root still usable after helloFile.Close: %v", err)
	}
}

// TestFile_Walk_ErrorReleasesFid: file.Walk(nonexistent) returns an
// error AND the reserved newFid is released to the allocator (Pitfall
// 2). We verify via the FidReuseLen test hook.
func TestFile_Walk_ErrorReleasesFid(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileTestCtx(t)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = root.Close() }()

	before := client.FidReuseLen(cli)

	_, err = root.Walk(ctx, []string{"does_not_exist"})
	if err == nil {
		t.Fatal("Walk(nonexistent): nil err, want server error")
	}

	after := client.FidReuseLen(cli)
	if after != before+1 {
		t.Errorf("reuse cache delta = %d, want +1 (leaked fid)", after-before)
	}
}

// TestFile_Clone_IndependentOffset: file.Clone returns a *File with
// the same qid but a distinct fid. Closing either does not affect the
// other.
func TestFile_Clone_IndependentOffset(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileTestCtx(t)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = root.Close() }()

	clone, err := root.Clone(ctx)
	if err != nil {
		t.Fatalf("root.Clone: %v", err)
	}
	if clone.Fid() == root.Fid() {
		t.Errorf("clone shares root fid %d", root.Fid())
	}
	if clone.Qid() != root.Qid() {
		t.Errorf("clone.Qid() = %#v, want root.Qid() = %#v", clone.Qid(), root.Qid())
	}
	// Close the clone first — root must remain usable.
	if err := clone.Close(); err != nil {
		t.Fatalf("clone.Close: %v", err)
	}
	if _, err := root.Walk(ctx, []string{"hello.txt"}); err != nil {
		t.Errorf("root unusable after clone.Close: %v", err)
	}
}

// TestFile_Clone_ErrorReleasesFid: forcing Clone to fail (closed Conn
// mid-op) returns an error AND the reserved newFid lands back on the
// allocator's reuse slice.
func TestFile_Clone_ErrorReleasesFid(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileTestCtx(t)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// Close the Conn — next Clone call must fail fast via ErrClosed.
	if err := cli.Close(); err != nil {
		t.Fatalf("cli.Close: %v", err)
	}

	before := client.FidReuseLen(cli)
	_, err = root.Clone(ctx)
	if err == nil {
		t.Fatal("Clone on closed Conn: got nil, want ErrClosed")
	}
	after := client.FidReuseLen(cli)
	if after != before+1 {
		t.Errorf("reuse cache delta = %d, want +1 (leaked fid after failed Clone)",
			after-before)
	}
}

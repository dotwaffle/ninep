package client_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
)

func sessionTestCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), 5*time.Second)
}

// TestConnAttach_Root: Conn.Attach returns a *File bound to the root
// fid with a QTDIR QID.
func TestConnAttach_Root(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := sessionTestCtx(t)
	defer cancel()

	f, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer f.Close()
	if f.Fid() == 0 {
		t.Errorf("Attach root has zero fid")
	}
	if f.Qid().Type&proto.QTDIR == 0 {
		t.Errorf("root Qid.Type = %#x, want QTDIR set", f.Qid().Type)
	}
}

// TestConnAttach_Close: after Attach+Close, the root file's fid is
// reusable; a second Attach succeeds.
func TestConnAttach_Close(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := sessionTestCtx(t)
	defer cancel()

	f1, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach 1: %v", err)
	}
	if err := f1.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}
	// Second Attach reuses the previously-allocated fid via the LIFO
	// reuse cache.
	f2, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach 2: %v", err)
	}
	defer f2.Close()
	if f2.Fid() != f1.Fid() {
		t.Errorf("second Attach fid = %d, want %d (LIFO reuse)", f2.Fid(), f1.Fid())
	}
}

// TestConnOpenFile_L: OpenFile against a .L server returns a *File
// whose fid is distinct from the root's.
func TestConnOpenFile_L(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := sessionTestCtx(t)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer root.Close()

	f, err := cli.OpenFile(ctx, "hello.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer f.Close()
	if f.Fid() == root.Fid() {
		t.Errorf("OpenFile fid collides with root fid %d", root.Fid())
	}
	if f.Qid().Type&proto.QTDIR != 0 {
		t.Errorf("hello.txt Qid.Type = %#x, want regular file (QTDIR unset)", f.Qid().Type)
	}
}

// TestConnOpenFile_WalkFailure_ReleasesFid: OpenFile on a nonexistent
// path returns an error and leaks no fid (Pitfall 2).
func TestConnOpenFile_WalkFailure_ReleasesFid(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := sessionTestCtx(t)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer root.Close()

	before := client.FidReuseLen(cli)
	_, err = cli.OpenFile(ctx, "nonexistent.txt", os.O_RDONLY, 0)
	if err == nil {
		t.Fatal("OpenFile nonexistent: nil err, want server error")
	}
	after := client.FidReuseLen(cli)
	if after != before+1 {
		t.Errorf("reuse cache delta = %d, want +1 (leaked fid)", after-before)
	}
}

// TestConnOpenFile_LopenFailure_ClunksAndReleases: when Walk succeeds
// but Lopen fails, the walked-to fid is Tclunked AND released. We
// provoke Lopen failure by calling OpenFile against a directory with
// O_WRONLY (memfs directories reject O_WRONLY).
func TestConnOpenFile_LopenFailure_ClunksAndReleases(t *testing.T) {
	t.Parallel()
	// Build a root with a subdirectory we can try to open for write.
	// buildTestRoot's root itself is a directory; walking to it with
	// 0-component path then Lopen(O_WRONLY) should fail.
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := sessionTestCtx(t)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer root.Close()

	before := client.FidReuseLen(cli)
	// Attempt to open root itself for WRONLY. Path "/" resolves to the
	// root directory -- splitPath returns nil so Walk is 0-step. Then
	// Lopen(O_WRONLY) should fail with EISDIR-style error.
	_, err = cli.OpenFile(ctx, "/", os.O_WRONLY, 0)
	if err == nil {
		t.Fatal("OpenFile(dir O_WRONLY): nil err, want server error")
	}
	after := client.FidReuseLen(cli)
	if after != before+1 {
		t.Errorf("reuse cache delta = %d, want +1 (leaked fid after Lopen failure)",
			after-before)
	}
}

// TestConnOpenFile_RequiresAttach: calling OpenFile before any Attach
// returns an explicit error rather than dereferencing a nil root.
func TestConnOpenFile_RequiresAttach(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := sessionTestCtx(t)
	defer cancel()

	_, err := cli.OpenFile(ctx, "hello.txt", os.O_RDONLY, 0)
	if err == nil {
		t.Fatal("OpenFile without Attach: nil err, want 'requires a prior Attach'")
	}
}

// TestConn_RootAccessor: Conn.Root() is nil before Attach and returns
// the most recent attach's *File after Attach.
func TestConn_RootAccessor(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := sessionTestCtx(t)
	defer cancel()

	if cli.Root() != nil {
		t.Errorf("Root() before Attach = %v, want nil", cli.Root())
	}
	attached, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer attached.Close()

	if got := cli.Root(); got != attached {
		t.Errorf("Root() after Attach = %p, want Attach return %p", got, attached)
	}
}

// TestConnOpenFile_AfterClosedConn: OpenFile against a closed Conn
// surfaces ErrClosed without leaking fids.
func TestConnOpenFile_AfterClosedConn(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := sessionTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := cli.Close(); err != nil {
		t.Fatalf("cli.Close: %v", err)
	}
	_, err := cli.OpenFile(ctx, "hello.txt", os.O_RDONLY, 0)
	if !errors.Is(err, client.ErrClosed) {
		t.Errorf("OpenFile on closed Conn: %v, want ErrClosed", err)
	}
}

package client_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
	"github.com/dotwaffle/ninep/server/memfs"
)

// symlinkTestCtx returns a 5s timeout context rooted at the test's context,
// matching rawAdvCtx's shape (see raw_advanced_test.go).
func symlinkTestCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), 5*time.Second)
}

// TestClient_Symlink_Creates: Conn.Symlink issues Tsymlink against the root
// directory, returns a stat-only *File whose QID.Type has the QTSYMLINK bit.
func TestClient_Symlink_Creates(t *testing.T) {
	t.Parallel()
	root := newTestRUDir(t)
	// Seed a target file so the symlink points somewhere meaningful; the
	// target string is an opaque server-side value, but this mirrors
	// realistic fixture shapes.
	gen := root.gen
	target := &memfs.StaticFile{Content: "hello"}
	target.Init(gen.Next(proto.QTFILE), target)
	root.AddChild("hello.txt", target.EmbeddedInode())

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	ctx, cancel := symlinkTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	f, err := cli.Symlink(ctx, "/link1", "hello.txt")
	if err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	defer func() { _ = f.Close() }()

	if f.Qid().Type&proto.QTSYMLINK == 0 {
		t.Errorf("Symlink QID.Type = %#x, missing QTSYMLINK bit", f.Qid().Type)
	}
}

// TestClient_Symlink_Subdir: Conn.Symlink walks into the existing subdir
// before issuing Tsymlink; the new entry appears there.
func TestClient_Symlink_Subdir(t *testing.T) {
	t.Parallel()
	root := newTestRUDir(t)
	// Add a subdir that itself is a testRUDir so the Symlink capability
	// fires at that level.
	subdir := &testRUDir{gen: root.gen}
	subdir.Init(root.gen.Next(proto.QTDIR), subdir)
	root.AddChild("subdir", subdir.EmbeddedInode())

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	ctx, cancel := symlinkTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	f, err := cli.Symlink(ctx, "/subdir/link2", "target")
	if err != nil {
		t.Fatalf("Symlink into subdir: %v", err)
	}
	defer func() { _ = f.Close() }()

	if f.Qid().Type&proto.QTSYMLINK == 0 {
		t.Errorf("Symlink QID.Type = %#x, missing QTSYMLINK bit", f.Qid().Type)
	}
	if _, ok := subdir.Children()["link2"]; !ok {
		t.Error("subdir child \"link2\" missing after Conn.Symlink")
	}
}

// TestClient_Symlink_MissingParent: Conn.Symlink into a nonexistent parent
// surfaces the server's ENOENT (wrapped as *client.Error) and leaves no fid
// in flight.
func TestClient_Symlink_MissingParent(t *testing.T) {
	t.Parallel()
	root := newTestRUDir(t)
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	ctx, cancel := symlinkTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	_, err := cli.Symlink(ctx, "/no-such-dir/link", "target")
	if err == nil {
		t.Fatal("Symlink into missing parent: want error, got nil")
	}
}

// TestClient_Readlink: existing symlink can be read via File.Readlink.
func TestClient_Readlink(t *testing.T) {
	t.Parallel()
	gen := &server.QIDGenerator{}
	root := memfs.NewDir(gen).AddSymlink("sym", "hello.txt")

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	ctx, cancel := symlinkTestCtx(t)
	defer cancel()

	rootF, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	_ = rootF

	// Use File.Walk to reach the symlink without opening it (9P has no
	// "open a symlink" — the fid is stat-only).
	f, err := rootF.Walk(ctx, []string{"sym"})
	if err != nil {
		t.Fatalf("Walk to sym: %v", err)
	}
	defer func() { _ = f.Close() }()

	got, err := f.Readlink(ctx)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if got != "hello.txt" {
		t.Errorf("Readlink = %q, want %q", got, "hello.txt")
	}
}

// TestClient_Readlink_NotSymlink: File.Readlink on a regular file surfaces a
// *client.Error from the server (Readlink is only meaningful on symlinks).
func TestClient_Readlink_NotSymlink(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := symlinkTestCtx(t)
	defer cancel()

	rootF, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	f, err := rootF.Walk(ctx, []string{"hello.txt"})
	if err != nil {
		t.Fatalf("Walk to hello.txt: %v", err)
	}
	defer func() { _ = f.Close() }()

	_, err = f.Readlink(ctx)
	if err == nil {
		t.Fatal("Readlink on regular file: want error, got nil")
	}
	var cerr *client.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("Readlink err = %v (%T), want *client.Error", err, err)
	}
}

// TestClient_Symlink_NotSupportedOnU: Conn.Symlink on a .u-negotiated Conn
// returns ErrNotSupported without touching the wire.
func TestClient_Symlink_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()

	ctx, cancel := symlinkTestCtx(t)
	defer cancel()
	// Attach first so Conn.Symlink's root-nil check doesn't fire before
	// the dialect gate.
	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	_, err := cli.Symlink(ctx, "/x", "target")
	if !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Symlink err = %v, want ErrNotSupported", err)
	}
}

// TestClient_Readlink_NotSupportedOnU: File.Readlink on a .u-negotiated
// Conn returns ErrNotSupported. Reuses the .u mock server pair and walks
// to a fake file to obtain a *File.
func TestClient_Readlink_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()

	ctx, cancel := symlinkTestCtx(t)
	defer cancel()
	rootF, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	// 0-step walk (Clone) reaches a stat-only fid; the dialect gate in
	// File.Readlink fires before any wire op.
	f, err := rootF.Clone(ctx)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Readlink(ctx); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Readlink err = %v, want ErrNotSupported", err)
	}
}

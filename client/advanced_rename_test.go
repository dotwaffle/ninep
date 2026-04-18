package client_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server/memfs"
)

func renameTestCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), 5*time.Second)
}

// TestClient_Rename_SameDir: rename within the same directory via
// Trenameat. Walk to the new name succeeds; walk to the old name fails.
func TestClient_Rename_SameDir(t *testing.T) {
	t.Parallel()
	root := newTestRUDir(t)
	gen := root.gen
	a := &memfs.MemFile{Data: []byte("hello\n")}
	a.Init(gen.Next(proto.QTFILE), a)
	root.AddChild("a.txt", a.EmbeddedInode())

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	ctx, cancel := renameTestCtx(t)
	defer cancel()

	rootF, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	if err := cli.Rename(ctx, "/a.txt", "/b.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, ok := root.Children()["b.txt"]; !ok {
		t.Error("b.txt missing after rename")
	}
	if _, ok := root.Children()["a.txt"]; ok {
		t.Error("a.txt still present after rename")
	}
	// Walk to old path should fail.
	if _, err := rootF.Walk(ctx, []string{"a.txt"}); err == nil {
		t.Error("Walk to post-rename old path: want error, got nil")
	}
}

// TestClient_Rename_CrossDir: cross-directory rename.
func TestClient_Rename_CrossDir(t *testing.T) {
	t.Parallel()
	root := newTestRUDir(t)
	gen := root.gen
	dir1 := &testRUDir{gen: gen}
	dir1.Init(gen.Next(proto.QTDIR), dir1)
	root.AddChild("dir1", dir1.EmbeddedInode())
	dir2 := &testRUDir{gen: gen}
	dir2.Init(gen.Next(proto.QTDIR), dir2)
	root.AddChild("dir2", dir2.EmbeddedInode())
	f := &memfs.MemFile{Data: []byte("moveme")}
	f.Init(gen.Next(proto.QTFILE), f)
	dir1.AddChild("a.txt", f.EmbeddedInode())

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	ctx, cancel := renameTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	if err := cli.Rename(ctx, "/dir1/a.txt", "/dir2/a.txt"); err != nil {
		t.Fatalf("Rename cross-dir: %v", err)
	}
	if _, ok := dir2.Children()["a.txt"]; !ok {
		t.Error("dir2/a.txt missing after cross-dir rename")
	}
	if _, ok := dir1.Children()["a.txt"]; ok {
		t.Error("dir1/a.txt still present after cross-dir rename")
	}
}

// TestClient_Rename_WhileOpen: Pitfall 5 proof. Open a file, rename it,
// and confirm Read from the original *File still returns the original
// contents — the fid remained bound to the inode, not the path.
func TestClient_Rename_WhileOpen(t *testing.T) {
	t.Parallel()
	root := newTestRUDir(t)
	gen := root.gen
	payload := []byte("hello world\n")
	f := &memfs.MemFile{Data: append([]byte(nil), payload...)}
	f.Init(gen.Next(proto.QTFILE), f)
	root.AddChild("a.txt", f.EmbeddedInode())

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	ctx, cancel := renameTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	openedF, err := cli.OpenFile(ctx, "/a.txt", 0, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = openedF.Close() }()

	if err := cli.Rename(ctx, "/a.txt", "/b.txt"); err != nil {
		t.Fatalf("Rename while open: %v", err)
	}

	// Read from the original *File — should return the payload.
	got, err := io.ReadAll(openedF)
	if err != nil {
		t.Fatalf("ReadAll after rename: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("ReadAll = %q, want %q", got, payload)
	}
}

// TestClient_Rename_Missing: rename from a nonexistent source surfaces
// an error; no fid is left in flight.
func TestClient_Rename_Missing(t *testing.T) {
	t.Parallel()
	root := newTestRUDir(t)
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	ctx, cancel := renameTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	reuseBefore := client.FidReuseLen(cli)
	err := cli.Rename(ctx, "/nope/a", "/also-nope/b")
	if err == nil {
		t.Fatal("Rename of missing source: want error, got nil")
	}
	// Fid accounting: no stuck fids. Reuse depth may have grown (we
	// released several fids during the failed walk), but it must not
	// shrink — that would indicate fids were re-handed out without a
	// release cycle completing.
	if got := client.FidReuseLen(cli); got < reuseBefore {
		t.Errorf("FidReuseLen dropped: before=%d after=%d", reuseBefore, got)
	}
}

// TestClient_Rename_NotSupportedOnU: .u dialect gates Rename to
// ErrNotSupported (execute-time decision — Raw.Trename is .L-only on
// this codec, and ninep's .u server does not decode Trename).
func TestClient_Rename_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()

	ctx, cancel := renameTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	err := cli.Rename(ctx, "/a", "/b")
	if !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Rename err = %v, want ErrNotSupported", err)
	}
}

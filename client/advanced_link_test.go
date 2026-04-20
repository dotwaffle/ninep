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

func linkTestCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), 5*time.Second)
}

// TestClient_Link: Conn.Link creates a hard link. The new name appears
// in the parent directory; reading it returns the same content as the
// source (hard-link semantics — both names refer to one inode).
func TestClient_Link(t *testing.T) {
	t.Parallel()
	root := newTestRUDir(t)
	gen := root.gen
	src := &memfs.MemFile{Data: []byte("hello world\n")}
	src.Init(gen.Next(proto.QTFILE), src)
	root.AddChild("src.txt", src.EmbeddedInode())

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	ctx, cancel := linkTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	if err := cli.Link(ctx, "/src.txt", "/hard.txt"); err != nil {
		t.Fatalf("Link: %v", err)
	}
	if _, ok := root.Children()["hard.txt"]; !ok {
		t.Error("hard.txt missing after Link")
	}

	// Open the new name and verify it reads back the same content.
	f, err := cli.OpenFile(ctx, "/hard.txt", 0, 0)
	if err != nil {
		t.Fatalf("OpenFile(hard.txt): %v", err)
	}
	defer func() { _ = f.Close() }()
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "hello world\n" {
		t.Errorf("hard link content = %q, want %q", got, "hello world\n")
	}
}

// TestClient_Link_NotSupportedOnU: Conn.Link on a .u-negotiated Conn
// returns ErrNotSupported.
func TestClient_Link_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()

	ctx, cancel := linkTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := cli.Link(ctx, "/src", "/hard"); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Link err = %v, want ErrNotSupported", err)
	}
}

// TestClient_Link_NoFidLeak: 100 create/link/unlink cycles leave the
// fid-reuse cache bounded.
func TestClient_Link_NoFidLeak(t *testing.T) {
	t.Parallel()
	root := newTestRUDir(t)
	gen := root.gen
	// Seed a single source file; linking it many times exercises the
	// repeated Link path without creating new inodes.
	src := &memfs.MemFile{Data: []byte("x")}
	src.Init(gen.Next(proto.QTFILE), src)
	root.AddChild("src", src.EmbeddedInode())

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	ctx, cancel := linkTestCtx(t)
	defer cancel()
	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	const iters = 100
	for i := range iters {
		name := "link-" + intName(i)
		if err := cli.Link(ctx, "/src", "/"+name); err != nil {
			t.Fatalf("Link iter %d: %v", i, err)
		}
		if err := cli.Remove(ctx, "/"+name); err != nil {
			t.Fatalf("Remove iter %d: %v", i, err)
		}
	}
	if got := client.FidReuseLen(cli); got > 1024 {
		t.Errorf("FidReuseLen = %d, want <= 1024 (reuse-cache cap)", got)
	}
}

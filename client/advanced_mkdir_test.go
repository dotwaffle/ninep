package client_test

import (
	"errors"
	"testing"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
)

// TestClient_Mkdir_CreatesDir: Conn.Mkdir creates a directory whose QID
// carries QTDIR.
func TestClient_Mkdir_CreatesDir(t *testing.T) {
	t.Parallel()
	root := newTestRUDir(t)
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	ctx, cancel := mknodTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	f, err := cli.Mkdir(ctx, "/", "dir1", proto.FileMode(0o0755), 0)
	if err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	defer func() { _ = f.Close() }()

	if f.Qid().Type&proto.QTDIR == 0 {
		t.Errorf("Mkdir QID.Type = %#x, want QTDIR set", f.Qid().Type)
	}
	if _, ok := root.Children()["dir1"]; !ok {
		t.Error("dir1 missing from parent after Mkdir")
	}
}

// TestClient_Mkdir_Subdir: Mkdir into a subdirectory walks first.
func TestClient_Mkdir_Subdir(t *testing.T) {
	t.Parallel()
	root := newTestRUDir(t)
	sub := &testRUDir{gen: root.gen}
	sub.Init(root.gen.Next(proto.QTDIR), sub)
	root.AddChild("parent", sub.EmbeddedInode())

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	ctx, cancel := mknodTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	f, err := cli.Mkdir(ctx, "/parent", "child", proto.FileMode(0o0755), 0)
	if err != nil {
		t.Fatalf("Mkdir subdir: %v", err)
	}
	defer func() { _ = f.Close() }()

	if _, ok := sub.Children()["child"]; !ok {
		t.Error("parent/child missing after Mkdir")
	}
}

// TestClient_Mkdir_NotSupportedOnU: .u-gated.
func TestClient_Mkdir_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()

	ctx, cancel := mknodTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if _, err := cli.Mkdir(ctx, "/", "x", proto.FileMode(0o0755), 0); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Mkdir err = %v, want ErrNotSupported", err)
	}
}

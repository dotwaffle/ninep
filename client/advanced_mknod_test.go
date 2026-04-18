package client_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
)

func mknodTestCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), 5*time.Second)
}

// TestClient_Mknod_CreatesNode: Conn.Mknod creates a device/fifo node.
// The returned *File handle is stat-only; the QID does NOT carry QTDIR.
func TestClient_Mknod_CreatesNode(t *testing.T) {
	t.Parallel()
	root := newTestRUDir(t)
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	ctx, cancel := mknodTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// Mode carries POSIX S_IFIFO (0o010000) | rw-rw-rw- (0o0666). The
	// test fixture (testRUDir.Mknod) ignores mode and constructs a
	// DeviceNode unconditionally, but the wire encoding still carries
	// the mode byte correctly.
	mode := proto.FileMode(0o010000 | 0o0666)
	f, err := cli.Mknod(ctx, "/", "fifo1", mode, 0, 0, 0)
	if err != nil {
		t.Fatalf("Mknod: %v", err)
	}
	defer func() { _ = f.Close() }()

	if f.Qid().Type&proto.QTDIR != 0 {
		t.Errorf("Mknod QID.Type = %#x, unexpected QTDIR bit", f.Qid().Type)
	}
	if _, ok := root.Children()["fifo1"]; !ok {
		t.Error("fifo1 missing from parent after Mknod")
	}
}

// TestClient_Mknod_Subdir: Mknod into a subdirectory walks first.
func TestClient_Mknod_Subdir(t *testing.T) {
	t.Parallel()
	root := newTestRUDir(t)
	sub := &testRUDir{gen: root.gen}
	sub.Init(root.gen.Next(proto.QTDIR), sub)
	root.AddChild("dev", sub.EmbeddedInode())

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	ctx, cancel := mknodTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	f, err := cli.Mknod(ctx, "/dev", "null", proto.FileMode(0o020666), 1, 3, 0)
	if err != nil {
		t.Fatalf("Mknod subdir: %v", err)
	}
	defer func() { _ = f.Close() }()

	if _, ok := sub.Children()["null"]; !ok {
		t.Error("dev/null missing after Mknod")
	}
}

// TestClient_Mknod_NotSupportedOnU: .u-gated.
func TestClient_Mknod_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()

	ctx, cancel := mknodTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if _, err := cli.Mknod(ctx, "/", "x", proto.FileMode(0), 0, 0, 0); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Mknod err = %v, want ErrNotSupported", err)
	}
}

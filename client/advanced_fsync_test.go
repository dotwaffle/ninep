package client_test

import (
	"errors"
	"testing"

	"github.com/dotwaffle/ninep/client"
)

// TestClient_Fsync_DispatchesToNode: File.Fsync on an opened fid with no
// FileHandle falls back to NodeFsyncer, matching server/bridge.go's
// handleFsync precedence (FileSyncer on the handle first, NodeFsyncer on
// the node second). testRUDir.Open returns a nil handle, so this
// exercises the node-level fallback path.
func TestClient_Fsync_DispatchesToNode(t *testing.T) {
	t.Parallel()
	root := newTestRUDir(t)
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	ctx, cancel := mknodTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	f, err := cli.OpenFile(ctx, "/", 0, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	if err := f.Fsync(ctx, false); err != nil {
		t.Fatalf("Fsync: %v", err)
	}
	if err := f.Fsync(ctx, true); err != nil {
		t.Fatalf("Fsync(dataSync): %v", err)
	}

	root.mu.Lock()
	calls := root.fsyncCalls
	root.mu.Unlock()
	if calls != 2 {
		t.Errorf("fsyncCalls = %d, want 2", calls)
	}
}

// TestClient_Fsync_RequiresOpenFid: Fsync on a fid that was never opened
// (e.g. a Walk-only handle) surfaces the server's EBADF.
func TestClient_Fsync_RequiresOpenFid(t *testing.T) {
	t.Parallel()
	root := newTestRUDir(t)
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	ctx, cancel := mknodTestCtx(t)
	defer cancel()

	rootF, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	if err := rootF.Fsync(ctx, false); err == nil {
		t.Fatal("Fsync on an unopened fid: want error, got nil")
	}
}

// TestClient_Fsync_NotSupportedOnU: .u-gated.
func TestClient_Fsync_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()

	ctx, cancel := mknodTestCtx(t)
	defer cancel()

	rootF, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := rootF.Fsync(ctx, false); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Fsync err = %v, want ErrNotSupported", err)
	}
}

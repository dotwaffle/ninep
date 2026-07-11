package client_test

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
	"github.com/dotwaffle/ninep/server/memfs"
)

// TestFileRefreshSize_PopulatesCachedSize: before RefreshSize,
// f.CachedSize == 0. After RefreshSize on /hello.txt (12 bytes),
// f.CachedSize == 12. Exercises
// the full Tgetattr (.L) -> attr.Size -> f.cachedSize path.
func TestFileRefreshSize_PopulatesCachedSize(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileIOTestCtx(t)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = root.Close() }()

	f, err := cli.OpenFile(ctx, "hello.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	if got := client.CachedSizeOf(f); got != 0 {
		t.Fatalf("pre-RefreshSize cachedSize = %d, want 0", got)
	}
	if err := f.RefreshSize(); err != nil {
		t.Fatalf("RefreshSize: %v", err)
	}
	if got := client.CachedSizeOf(f); got != 12 {
		t.Errorf("post-RefreshSize cachedSize = %d, want 12", got)
	}
}

// TestFileRefreshSize_SeekEndAfterRefresh: after Sync, Seek(0, io.SeekEnd) returns
// the file's actual size, without requiring the SetCachedSize test
// hook.
func TestFileRefreshSize_SeekEndAfterRefresh(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileIOTestCtx(t)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = root.Close() }()

	f, err := cli.OpenFile(ctx, "hello.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	if err := f.RefreshSize(); err != nil {
		t.Fatalf("RefreshSize: %v", err)
	}
	pos, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		t.Fatalf("Seek(0, SeekEnd): %v", err)
	}
	if pos != 12 {
		t.Errorf("Seek(0, SeekEnd) after RefreshSize = %d, want 12", pos)
	}
}

// TestFileRefreshSize_IsIdempotent: consecutive Sync calls both succeed. Each
// call is a fresh wire op (no caching). Exercises that RefreshSize is safe to
// invoke repeatedly without state corruption.
func TestFileRefreshSize_IsIdempotent(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileIOTestCtx(t)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = root.Close() }()

	f, err := cli.OpenFile(ctx, "hello.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	for i := range 3 {
		if err := f.RefreshSize(); err != nil {
			t.Errorf("RefreshSize #%d: %v, want nil", i+1, err)
		}
	}
	if got := client.CachedSizeOf(f); got != 12 {
		t.Errorf("post-RefreshSize cachedSize = %d, want 12", got)
	}
}

// TestFileRefreshSize_ErrorPropagates: Sync against a node whose Getattr
// returns proto.ENOENT surfaces a *client.Error whose errors.Is matches
// proto.ENOENT. The pre-call cachedSize is preserved on error (not
// zeroed by a failed RefreshSize).
func TestFileRefreshSize_ErrorPropagates(t *testing.T) {
	t.Parallel()
	gen := &server.QIDGenerator{}
	root := memfs.NewDir(gen)
	node := &testSyncENOENT{qid: gen.Next(proto.QTFILE)}
	node.Init(node.qid, node)
	root.AddChild("broken.txt", &node.Inode)

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()
	ctx, cancel := fileIOTestCtx(t)
	defer cancel()

	r, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = r.Close() }()

	f, err := cli.OpenFile(ctx, "broken.txt", os.O_RDONLY, 0)
	if err != nil {
		// Open itself may surface ENOENT via the bridge's Getattr path.
		var ce *client.Error
		if !errors.As(err, &ce) || !errors.Is(err, proto.ENOENT) {
			t.Fatalf("OpenFile err = %v, want *client.Error wrapping ENOENT", err)
		}
		return
	}
	defer func() { _ = f.Close() }()

	// Pre-poke cachedSize so we can assert it is NOT overwritten on
	// error (the contract: failed RefreshSize preserves the previous value).
	client.SetCachedSize(f, 99)
	err = f.RefreshSize()
	var ce *client.Error
	if !errors.As(err, &ce) {
		t.Fatalf("RefreshSize err = %v, want *client.Error", err)
	}
	if !errors.Is(err, proto.ENOENT) {
		t.Fatalf("errors.Is(RefreshSize err, ENOENT) = false, err = %v", err)
	}
	if got := client.CachedSizeOf(f); got != 99 {
		t.Errorf("post-error cachedSize = %d, want 99 (error path must preserve)", got)
	}
}

// TestFileRefreshSize_DotU_UsesTstat: on a .u Conn, Sync issues Tstat (not
// Tgetattr) and populates cachedSize from Stat.Length. Exercised via
// the uMockStatServer that returns wantStat.Length == 12.
func TestFileRefreshSize_DotU_UsesTstat(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockStatClientPair(t, wantStat)
	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	if _, err := cli.Raw().Tattach(ctx, 0, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	f := client.NewFileForTest(cli)
	if err := f.RefreshSize(); err != nil {
		t.Fatalf("RefreshSize: %v", err)
	}
	if got := client.CachedSizeOf(f); got != 12 {
		t.Errorf("post-RefreshSize cachedSize = %d, want 12 (from wantStat.Length)", got)
	}
}

// testSyncENOENT is a memfs-compatible Node whose Getattr returns
// proto.ENOENT. Used to exercise RefreshSize's error-propagation path.
type testSyncENOENT struct {
	server.Inode
	qid proto.QID
}

func (n *testSyncENOENT) QID() proto.QID { return n.qid }

func (n *testSyncENOENT) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return nil, 0, nil
}

func (n *testSyncENOENT) Getattr(_ context.Context, _ proto.AttrMask) (proto.Attr, error) {
	return proto.Attr{}, proto.ENOENT
}

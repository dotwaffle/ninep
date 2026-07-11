package client_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

func TestFileOperationAfterCloseDoesNotUseRecycledFid(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	first, err := cli.Attach(t.Context(), "first", "")
	if err != nil {
		t.Fatalf("first Attach: %v", err)
	}
	retiredFid := first.Fid()
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := cli.Attach(t.Context(), "second", "")
	if err != nil {
		t.Fatalf("second Attach: %v", err)
	}
	defer func() { _ = second.Close() }()
	if second.Fid() != retiredFid {
		t.Fatalf("second fid = %d, want recycled fid %d", second.Fid(), retiredFid)
	}

	if _, err := first.Walk(t.Context(), []string{"hello.txt"}); !errors.Is(err, client.ErrClosed) {
		t.Fatalf("Walk on closed File error = %v, want ErrClosed", err)
	}
}

func TestFileMethodsRejectClosedFile(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	f, err := cli.Attach(t.Context(), "first", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	replacement, err := cli.Attach(t.Context(), "replacement", "")
	if err != nil {
		t.Fatalf("replacement Attach: %v", err)
	}
	defer func() { _ = replacement.Close() }()

	tests := []struct {
		name string
		call func() error
	}{
		{"Clone", func() error {
			clone, err := f.Clone(t.Context())
			if clone != nil {
				_ = clone.Close()
			}
			return err
		}},
		{"Stat", func() error { _, err := f.Stat(t.Context()); return err }},
		{"Getattr", func() error { _, err := f.Getattr(t.Context(), proto.AttrBasic); return err }},
		{"Setattr", func() error { return f.Setattr(t.Context(), proto.SetAttr{}) }},
		{"RefreshSize", f.RefreshSize},
		{"Read", func() error { _, err := f.Read(make([]byte, 1)); return err }},
		{"Write", func() error { _, err := f.Write([]byte{1}); return err }},
		{"Seek", func() error { _, err := f.Seek(0, 0); return err }},
		{"ReadAt", func() error { _, err := f.ReadAt(make([]byte, 1), 0); return err }},
		{"WriteAt", func() error { _, err := f.WriteAt([]byte{1}, 0); return err }},
		{"ReadDir", func() error { _, err := f.ReadDir(1); return err }},
		{"Fsync", func() error { return f.Fsync(t.Context(), false) }},
		{"Statfs", func() error { _, err := f.Statfs(t.Context()); return err }},
		{"Readlink", func() error { _, err := f.Readlink(t.Context()); return err }},
		{"Lock", func() error { return f.Lock(t.Context(), client.LockRead) }},
		{"Unlock", func() error { return f.Unlock(t.Context()) }},
		{"TryLock", func() error { _, err := f.TryLock(t.Context(), client.LockRead); return err }},
		{"GetLock", func() error { _, err := f.GetLock(t.Context(), client.LockRead); return err }},
		{"XattrGet", func() error { _, err := f.XattrGet(t.Context(), "user.test"); return err }},
		{"XattrSet", func() error { return f.XattrSet(t.Context(), "user.test", nil, 0) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); !errors.Is(err, client.ErrClosed) {
				t.Fatalf("error = %v, want ErrClosed", err)
			}
		})
	}
}

func TestConnPathOperationRejectsClosedRoot(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	root, err := cli.Attach(t.Context(), "first", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	retiredFid := root.Fid()
	if err := root.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw := cli.Raw()
	reused, err := raw.AcquireFid()
	if err != nil {
		t.Fatalf("AcquireFid: %v", err)
	}
	if reused != retiredFid {
		t.Fatalf("AcquireFid = %d, want recycled root fid %d", reused, retiredFid)
	}
	if _, err := raw.Tattach(t.Context(), reused, "raw", ""); err != nil {
		t.Fatalf("raw Tattach: %v", err)
	}
	defer func() {
		_ = raw.Tclunk(t.Context(), reused)
		raw.ReleaseFid(reused)
	}()

	if _, err := cli.OpenFile(t.Context(), "/hello.txt", 0, 0); !errors.Is(err, client.ErrClosed) {
		t.Fatalf("OpenFile with closed implicit root error = %v, want ErrClosed", err)
	}
}

type parallelReadNode struct {
	server.Inode
	handle *parallelReadHandle
}

func (n *parallelReadNode) Open(context.Context, uint32) (server.FileHandle, uint32, error) {
	return n.handle, 0, nil
}

type parallelReadHandle struct {
	entered chan uint64
	release chan struct{}
}

func (h *parallelReadHandle) Read(ctx context.Context, buf []byte, offset uint64) (int, error) {
	select {
	case h.entered <- offset:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	select {
	case <-h.release:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	buf[0] = byte(offset)
	return 1, nil
}

func TestFileReadAtRunsConcurrentlyOnOneFile(t *testing.T) {
	t.Parallel()
	h := &parallelReadHandle{
		entered: make(chan uint64, 2),
		release: make(chan struct{}),
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(h.release) }) }
	defer release()

	gen := &server.QIDGenerator{}
	root := &struct{ server.Inode }{}
	root.Init(gen.Next(proto.QTDIR), root)
	node := &parallelReadNode{handle: h}
	node.Init(gen.Next(proto.QTFILE), node)
	root.AddChild("parallel", node.EmbeddedInode())

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()
	if _, err := cli.Attach(t.Context(), "reader", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	f, err := cli.OpenFile(t.Context(), "/parallel", 0, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	errCh := make(chan error, 2)
	for off := int64(0); off < 2; off++ {
		go func() {
			buf := make([]byte, 1)
			_, err := f.ReadAt(buf, off)
			errCh <- err
		}()
	}

	for range 2 {
		select {
		case <-h.entered:
		case <-time.After(500 * time.Millisecond):
			t.Fatal("concurrent ReadAt calls did not both reach the server")
		}
	}
	release()
	for range 2 {
		if err := <-errCh; err != nil {
			t.Errorf("ReadAt: %v", err)
		}
	}
}

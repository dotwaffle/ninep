package client

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

// wedgedReleaseFile is a file whose handle Release parks until the request
// context is cancelled, delaying the server's Rclunk past any client-side
// deadline.
type wedgedReleaseFile struct {
	server.Inode
	release <-chan struct{}
}

func (f *wedgedReleaseFile) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return &wedgedHandle{release: f.release}, 0, nil
}

type wedgedHandle struct {
	release <-chan struct{}
}

func (h *wedgedHandle) Release(context.Context) error {
	<-h.release
	return nil
}

// TestFileClose_TimedOutClunkLeaksFidNumber asserts File.Close does NOT
// return the fid to the allocator when the Tclunk times out: the server's
// view is unknown, so handing the number to a new caller would alias its
// Twalk onto a fid the server may still hold. The number is leaked
// instead. Internal test: it inspects the allocator's reuse cache.
//
// Runtime note: the clunk deadline is the fixed 5s cleanupDeadline, so
// this test takes ~5s wall time; it runs in parallel with the rest of the
// suite.
func TestFileClose_TimedOutClunkLeaksFidNumber(t *testing.T) {
	t.Parallel()

	gen := &server.QIDGenerator{}
	root := &struct{ server.Inode }{}
	root.Init(gen.Next(proto.QTDIR), root)
	release := make(chan struct{})
	wedged := &wedgedReleaseFile{release: release}
	wedged.Init(gen.Next(proto.QTFILE), wedged)
	root.AddChild("wedged", wedged.EmbeddedInode())

	cliNC, srvNC := net.Pipe()
	srv := server.MustNew(root,
		server.WithMaxMsize(65536),
		server.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	srvCtx, srvCancel := context.WithCancel(t.Context())
	srvDone := make(chan struct{})
	go func() { defer close(srvDone); srv.ServeConn(srvCtx, srvNC) }()
	t.Cleanup(func() { srvCancel(); _ = srvNC.Close(); <-srvDone })
	t.Cleanup(func() { close(release) })

	dialCtx, dialCancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer dialCancel()
	cli, err := Dial(dialCtx, cliNC,
		WithMsize(65536),
		WithCleanupTimeout(50*time.Millisecond),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	if _, err := cli.Attach(t.Context(), "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	f, err := cli.OpenFile(t.Context(), "/wedged", 0, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	fid := f.Fid()

	// Keep the ordinary cancellation flush grace much longer than the
	// cleanup timeout. Cleanup clunks must not inherit that extra wait.
	cli.flushGrace = 2 * time.Second
	started := time.Now()
	err = f.Close()
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("Close succeeded; expected a deadline error from the wedged Rclunk")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Close took %v; cleanup timeout was extended by flush grace", elapsed)
	}
	if errors.Is(err, ErrClosed) {
		t.Fatalf("Close error = %v; conn closed underneath the test", err)
	}

	cli.fids.mu.Lock()
	leaked := !slices.Contains(cli.fids.reuse, fid)
	cli.fids.mu.Unlock()
	if !leaked {
		t.Errorf("fid %d returned to the reuse cache after a timed-out clunk; a new caller could alias the still-bound server fid", fid)
	}

	// A fresh acquisition must not hand out the leaked number.
	got, err := cli.fids.acquire()
	if err != nil {
		t.Fatalf("acquire after leak: %v", err)
	}
	if got == fid {
		t.Errorf("allocator handed out the leaked fid %d", fid)
	}
}

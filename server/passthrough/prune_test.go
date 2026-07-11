//go:build linux || freebsd

package passthrough

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/server"
)

// newPruneTestServer starts a passthrough server for root over an in-memory
// pipe and returns a dialed client plus a teardown func. root is deliberately
// NOT closed by the caller: Tattach binds a fid to root itself, so the
// server's own connection teardown (triggered by closing the client) clunks
// that fid and calls root.Close via NodeCloser -- closing root's fd a second
// time here would race an already-freed (and possibly reused) fd number.
// teardown closes the client connection and waits for the server goroutine
// to finish that teardown before returning, so callers can rely on root's fd
// having been released by the time teardown returns.
func newPruneTestServer(t *testing.T, root *Root) (*client.Conn, func()) {
	t.Helper()

	srv := server.New(root)
	cliNC, srvNC := net.Pipe()

	runCtx, runCancel := context.WithTimeout(t.Context(), 5*time.Second)

	srvDone := make(chan struct{})
	go func() {
		defer close(srvDone)
		srv.ServeConn(runCtx, srvNC)
	}()

	cli, err := client.Dial(runCtx, cliNC)
	if err != nil {
		runCancel()
		<-srvDone
		t.Fatalf("Dial: %v", err)
	}

	teardown := func() {
		_ = cli.Close()
		runCancel()
		<-srvDone
	}
	return cli, teardown
}

// TestPrune_WalkThenClunkRemovesChildEntry proves the basic refcounted-prune
// path: a single fid walked to a name, then clunked, removes that name from
// the parent Inode's children map instead of retaining it forever (the
// pre-refcounting behavior).
func TestPrune_WalkThenClunkRemovesChildEntry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write file.txt: %v", err)
	}

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatalf("NewRoot(%q): %v", dir, err)
	}

	cli, teardown := newPruneTestServer(t, root)
	defer teardown()

	rootF, err := cli.Attach(t.Context(), "nobody", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	f, err := rootF.Walk(t.Context(), []string{"file.txt"})
	if err != nil {
		t.Fatalf("Walk(file.txt): %v", err)
	}

	if _, ok := root.EmbeddedInode().Children()["file.txt"]; !ok {
		t.Fatal("children map missing file.txt after walk")
	}

	// Clunk's Rclunk response only arrives once the server has run
	// handleClunk (and therefore decRefNode) to completion, so the
	// children map is already updated by the time Close returns.
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, ok := root.EmbeddedInode().Children()["file.txt"]; ok {
		t.Error("children map still has file.txt after the only fid clunked; want pruned")
	}
}

//go:build linux || freebsd

package fstest

import (
	"bytes"
	"io"
	"os"
	"sync"
	"testing"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/client/clienttest"
	"github.com/dotwaffle/ninep/server"
	"github.com/dotwaffle/ninep/server/memfs"
)

// TestConcurrentWrite drives genuinely concurrent writes through a real
// client against a single file. Each worker opens its own fid and writes
// a distinct, non-overlapping region, so the Twrites are in flight at
// once over the multiplexed connection. The file must end up holding
// every region intact, which exercises the node-level write locking the
// in-memory roots rely on. Run under -race.
//
// This lives outside the shared Cases slice: the protocol-level Cases run
// over a raw net.Pipe that cannot multiplex, whereas concurrency needs a
// real client.Conn, and the fstest core deliberately avoids importing
// clienttest/memfs into its non-test surface.
func TestConcurrentWrite(t *testing.T) {
	t.Parallel()

	t.Run("builtin", func(t *testing.T) {
		t.Parallel()
		_, cli := clienttest.Pair(t, NewTestTree(&server.QIDGenerator{}))
		runConcurrentWrite(t, cli)
	})
	t.Run("memfs", func(t *testing.T) {
		t.Parallel()
		_, cli := clienttest.MemfsPair(t, func(*memfs.MemDir) {})
		runConcurrentWrite(t, cli)
	})
}

func runConcurrentWrite(t *testing.T, cli *client.Conn) {
	t.Helper()
	ctx := t.Context()

	if _, err := cli.Attach(ctx, "test", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// Create the target once, then close; the workers reopen it.
	f, err := cli.Create(ctx, "concurrent.bin", os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close after Create: %v", err)
	}

	const workers, chunk = 8, 64
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			wf, err := cli.OpenFile(ctx, "concurrent.bin", os.O_RDWR, 0)
			if err != nil {
				errs[i] = err
				return
			}
			defer func() { _ = wf.Close() }()
			buf := bytes.Repeat([]byte{byte('A' + i)}, chunk)
			if _, err := wf.WriteAt(buf, int64(i*chunk)); err != nil {
				errs[i] = err
			}
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}

	rf, err := cli.OpenFile(ctx, "concurrent.bin", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile read: %v", err)
	}
	defer func() { _ = rf.Close() }()
	got := make([]byte, workers*chunk)
	if _, err := io.ReadFull(rf, got); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	for i := range workers {
		want := bytes.Repeat([]byte{byte('A' + i)}, chunk)
		if region := got[i*chunk : (i+1)*chunk]; !bytes.Equal(region, want) {
			t.Errorf("region %d = %q, want %q", i, region, want)
		}
	}
}

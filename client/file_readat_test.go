package client_test

import (
	"context"
	"errors"
	"io"
	"math/rand"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
)

func fileReadAtTestCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), 5*time.Second)
}

// TestFileReadAt_Full: ReadAt exactly len(p) bytes starting at 0 on
// the 12-byte hello.txt fixture -- returns (12, nil) with expected
// content.
func TestFileReadAt_Full(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileReadAtTestCtx(t)
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

	buf := make([]byte, 12)
	n, err := f.ReadAt(buf, 0)
	if err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if n != 12 {
		t.Errorf("ReadAt n=%d, want 12", n)
	}
	if string(buf) != "hello world\n" {
		t.Errorf("ReadAt = %q, want %q", buf, "hello world\n")
	}
}

// TestFileReadAt_EOFShortFill: ReadAt len 20 on a 12-byte file returns
// (12, io.EOF) -- contract requires non-nil error when n < len(p).
func TestFileReadAt_EOFShortFill(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileReadAtTestCtx(t)
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

	buf := make([]byte, 20)
	n, err := f.ReadAt(buf, 0)
	if n != 12 {
		t.Errorf("ReadAt short fill n=%d, want 12", n)
	}
	if !errors.Is(err, io.EOF) {
		t.Errorf("ReadAt short fill err=%v, want io.EOF", err)
	}
	if string(buf[:n]) != "hello world\n" {
		t.Errorf("ReadAt short fill content = %q", buf[:n])
	}
}

// TestFileReadAt_DoesNotMutateOffset: Seek(5) then ReadAt(buf, 0);
// f.offset must still be 5 afterward. Asserts io.ReaderAt invariant.
func TestFileReadAt_DoesNotMutateOffset(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileReadAtTestCtx(t)
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

	if _, err := f.Seek(5, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := f.ReadAt(buf, 0); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	pos, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatalf("Seek current: %v", err)
	}
	if pos != 5 {
		t.Errorf("offset after ReadAt = %d, want 5 (ReadAt must not mutate)", pos)
	}
}

// TestFileReadAt_NegativeOffset: ReadAt(buf, -1) returns a non-nil
// error; buf is untouched.
func TestFileReadAt_NegativeOffset(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileReadAtTestCtx(t)
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

	buf := make([]byte, 4)
	n, err := f.ReadAt(buf, -1)
	if err == nil {
		t.Fatal("ReadAt(-1): nil err, want negative-offset error")
	}
	if n != 0 {
		t.Errorf("ReadAt(-1): n=%d, want 0", n)
	}
	if !strings.Contains(err.Error(), "negative") {
		t.Errorf("ReadAt(-1) err=%q, want mention of 'negative'", err)
	}
}

// TestFileReadAt_Concurrent_Race: 8 goroutines x 100 iterations each
// calling ReadAt at random offsets within the file. Under -race, no
// data race. Each goroutine verifies its observed bytes match the
// file content at the requested offset.
//
// Per D-12 ReadAt serializes under f.mu; this test primarily proves
// "no race, correct serialization," not parallel performance.
func TestFileReadAt_Concurrent_Race(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileReadAtTestCtx(t)
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

	content := []byte("hello world\n")
	const G = 8
	const iters = 100
	var wg sync.WaitGroup
	errs := make(chan error, G*iters)
	for gi := range G {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			r := rand.New(rand.NewSource(seed))
			for range iters {
				// Pick offset in [0, 12); read 1..(12-off) bytes.
				off := r.Intn(len(content))
				maxLen := len(content) - off
				n := 1 + r.Intn(maxLen)
				buf := make([]byte, n)
				got, err := f.ReadAt(buf, int64(off))
				if err != nil && !errors.Is(err, io.EOF) {
					errs <- err
					return
				}
				if got != n {
					errs <- errors.New("short read")
					return
				}
				for k := range n {
					if buf[k] != content[off+k] {
						errs <- errors.New("mismatch")
						return
					}
				}
			}
		}(int64(42 + gi))
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("goroutine error: %v", e)
	}
}

// TestFileReadAt_ChunkedLargeFile: ReadAt against a file larger than
// maxChunk() loops internally and returns the full buffer. Exercises
// the multi-Tread path.
func TestFileReadAt_ChunkedLargeFile(t *testing.T) {
	t.Parallel()
	const msize = 8192
	cli, cleanup := newClientServerPair(t, buildTestRoot(t),
		client.WithMsize(msize),
	)
	defer cleanup()
	ctx, cancel := fileReadAtTestCtx(t)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = root.Close() }()

	// Populate rw.bin with a 100 KiB payload.
	payload := make([]byte, 100*1024)
	for i := range payload {
		payload[i] = byte((i * 31) % 251)
	}
	{
		wf, err := cli.OpenFile(ctx, "rw.bin", os.O_RDWR, 0)
		if err != nil {
			t.Fatalf("OpenFile write: %v", err)
		}
		if _, err := wf.Write(payload); err != nil {
			_ = wf.Close()
			t.Fatalf("Write fixture: %v", err)
		}
		_ = wf.Close()
	}

	rf, err := cli.OpenFile(ctx, "rw.bin", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile read: %v", err)
	}
	defer func() { _ = rf.Close() }()

	// Assert ReadAt MUST chunk by verifying len(buf) > maxChunk.
	if chunk := client.MaxChunk(rf); uint32(len(payload)) <= chunk {
		t.Fatalf("test precondition: payload=%d <= maxChunk=%d; bump msize or payload",
			len(payload), chunk)
	}

	buf := make([]byte, len(payload))
	n, err := rf.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt: %v", err)
	}
	if n != len(payload) {
		t.Errorf("ReadAt n=%d, want %d", n, len(payload))
	}
	for i := range buf {
		if buf[i] != payload[i] {
			t.Fatalf("mismatch at byte %d: got %d want %d", i, buf[i], payload[i])
		}
	}
}

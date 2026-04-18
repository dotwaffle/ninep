package client_test

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
)

func fileWriteAtTestCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), 5*time.Second)
}

// TestFileWriteAt_Basic: WriteAt("abcd", 10) returns (4, nil); a
// subsequent ReadAt at offset 10 returns "abcd".
func TestFileWriteAt_Basic(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileWriteAtTestCtx(t)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = root.Close() }()

	f, err := cli.OpenFile(ctx, "rw.bin", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	n, err := f.WriteAt([]byte("abcd"), 10)
	if err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	if n != 4 {
		t.Errorf("WriteAt n=%d, want 4", n)
	}
	// Read back via a fresh handle to bypass any File-state coupling.
	rf, err := cli.OpenFile(ctx, "rw.bin", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile read-back: %v", err)
	}
	defer func() { _ = rf.Close() }()
	buf := make([]byte, 4)
	rn, err := rf.ReadAt(buf, 10)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt read-back: %v", err)
	}
	if rn != 4 {
		t.Errorf("read-back n=%d, want 4", rn)
	}
	if string(buf) != "abcd" {
		t.Errorf("read-back = %q, want %q", buf, "abcd")
	}
}

// TestFileWriteAt_DoesNotMutateOffset: Seek(5) then WriteAt(buf, 100);
// f.offset must still be 5 afterward.
func TestFileWriteAt_DoesNotMutateOffset(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileWriteAtTestCtx(t)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = root.Close() }()

	f, err := cli.OpenFile(ctx, "rw.bin", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Seek(5, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if _, err := f.WriteAt([]byte("xyz"), 100); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	pos, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatalf("Seek current: %v", err)
	}
	if pos != 5 {
		t.Errorf("offset after WriteAt = %d, want 5 (WriteAt must not mutate)", pos)
	}
}

// TestFileWriteAt_Chunked: write 100 KiB at off=0 against a small
// msize so the Twrite loop iterates multiple times. Verify total
// written equals len(p) AND len(p) > maxChunk.
func TestFileWriteAt_Chunked(t *testing.T) {
	t.Parallel()
	const msize = 8192
	cli, cleanup := newClientServerPair(t, buildTestRoot(t),
		client.WithMsize(msize),
	)
	defer cleanup()
	ctx, cancel := fileWriteAtTestCtx(t)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = root.Close() }()

	f, err := cli.OpenFile(ctx, "rw.bin", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	payload := make([]byte, 100*1024)
	for i := range payload {
		payload[i] = byte((i * 17) % 251)
	}
	if chunk := client.MaxChunk(f); uint32(len(payload)) <= chunk {
		t.Fatalf("test precondition: payload=%d <= maxChunk=%d",
			len(payload), chunk)
	}
	n, err := f.WriteAt(payload, 0)
	if err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	if n != len(payload) {
		t.Errorf("WriteAt n=%d, want %d", n, len(payload))
	}

	// Read back and verify.
	rf, err := cli.OpenFile(ctx, "rw.bin", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile read-back: %v", err)
	}
	defer func() { _ = rf.Close() }()
	got := make([]byte, len(payload))
	rn, err := rf.ReadAt(got, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt: %v", err)
	}
	if rn != len(payload) {
		t.Fatalf("ReadAt n=%d, want %d", rn, len(payload))
	}
	for i := range got {
		if got[i] != payload[i] {
			t.Fatalf("mismatch at byte %d: got %d want %d", i, got[i], payload[i])
		}
	}
}

// TestFileWriteAt_NegativeOffset: WriteAt(buf, -1) returns a non-nil
// error matching "negative"; no wire op issued.
func TestFileWriteAt_NegativeOffset(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileWriteAtTestCtx(t)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = root.Close() }()

	f, err := cli.OpenFile(ctx, "rw.bin", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	n, err := f.WriteAt([]byte("data"), -1)
	if err == nil {
		t.Fatal("WriteAt(-1): nil err, want negative-offset error")
	}
	if n != 0 {
		t.Errorf("WriteAt(-1): n=%d, want 0", n)
	}
	if !strings.Contains(err.Error(), "negative") {
		t.Errorf("WriteAt(-1) err=%q, want mention of 'negative'", err)
	}
}

// TestFileWriteAt_EmptyBuffer: WriteAt(nil/zero-len, off) returns
// (0, nil) without a wire op.
func TestFileWriteAt_EmptyBuffer(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileWriteAtTestCtx(t)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = root.Close() }()

	f, err := cli.OpenFile(ctx, "rw.bin", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	for _, name := range []string{"nil", "zero-len"} {
		var buf []byte
		if name == "zero-len" {
			buf = make([]byte, 0)
		}
		n, err := f.WriteAt(buf, 0)
		if err != nil {
			t.Errorf("WriteAt(%s): err=%v, want nil", name, err)
		}
		if n != 0 {
			t.Errorf("WriteAt(%s): n=%d, want 0", name, n)
		}
	}
}

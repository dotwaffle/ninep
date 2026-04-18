package client_test

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
)

// fileIOTestCtx returns a 5s timeout ctx for file I/O tests.
func fileIOTestCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), 5*time.Second)
}

// TestFileRead_Sequential: three 4-byte reads against the 12-byte
// "hello world\n" fixture return the expected chunks in order, f.offset
// advances, and the fourth read returns (0, io.EOF).
func TestFileRead_Sequential(t *testing.T) {
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

	want := []string{"hell", "o wo", "rld\n"}
	for i, w := range want {
		buf := make([]byte, 4)
		n, err := f.Read(buf)
		if err != nil {
			t.Fatalf("Read #%d: %v", i, err)
		}
		if n != 4 {
			t.Errorf("Read #%d: n=%d, want 4", i, n)
		}
		if string(buf[:n]) != w {
			t.Errorf("Read #%d: got %q, want %q", i, buf[:n], w)
		}
	}
	// Fourth read: at EOF.
	buf := make([]byte, 4)
	n, err := f.Read(buf)
	if !errors.Is(err, io.EOF) {
		t.Errorf("Read #4: err=%v, want io.EOF", err)
	}
	if n != 0 {
		t.Errorf("Read #4: n=%d, want 0", n)
	}
}

// TestFileRead_EmptyBuffer: Read(nil) and Read(zero-len) return
// (0, nil) without contacting the server.
func TestFileRead_EmptyBuffer(t *testing.T) {
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

	for _, name := range []string{"nil", "zero-len"} {
		var buf []byte
		if name == "zero-len" {
			buf = make([]byte, 0)
		}
		n, err := f.Read(buf)
		if err != nil {
			t.Errorf("Read(%s buffer): err=%v, want nil", name, err)
		}
		if n != 0 {
			t.Errorf("Read(%s buffer): n=%d, want 0", name, n)
		}
	}
}

// TestFileRead_EOFAtEnd: Seek to file end and Read returns (0, io.EOF).
func TestFileRead_EOFAtEnd(t *testing.T) {
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

	// "hello world\n" = 12 bytes. Seek past the end.
	if _, err := f.Seek(12, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	buf := make([]byte, 8)
	n, err := f.Read(buf)
	if !errors.Is(err, io.EOF) {
		t.Errorf("Read at EOF: err=%v, want io.EOF", err)
	}
	if n != 0 {
		t.Errorf("Read at EOF: n=%d, want 0", n)
	}
}

// TestFileRead_OffsetAdvances: after a successful Read of n bytes,
// f.offset advances by n. Observable via Seek(0, SeekCurrent).
func TestFileRead_OffsetAdvances(t *testing.T) {
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

	buf := make([]byte, 5)
	n, err := f.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if n != 5 {
		t.Fatalf("Read: n=%d, want 5", n)
	}
	pos, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatalf("Seek(0, SeekCurrent): %v", err)
	}
	if pos != 5 {
		t.Errorf("f.offset after Read = %d, want 5", pos)
	}
}

// TestFileWrite_Sequential: Write("hi") then Write("there") produces
// "hithere" at offset 0, with offset advancing by the written bytes.
func TestFileWrite_Sequential(t *testing.T) {
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

	f, err := cli.OpenFile(ctx, "rw.bin", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("OpenFile rw.bin: %v", err)
	}
	defer func() { _ = f.Close() }()

	n, err := f.Write([]byte("hi"))
	if err != nil {
		t.Fatalf("Write 1: %v", err)
	}
	if n != 2 {
		t.Errorf("Write 1: n=%d, want 2", n)
	}
	n, err = f.Write([]byte("there"))
	if err != nil {
		t.Fatalf("Write 2: %v", err)
	}
	if n != 5 {
		t.Errorf("Write 2: n=%d, want 5", n)
	}
	pos, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if pos != 7 {
		t.Errorf("offset after Write = %d, want 7", pos)
	}
	// Read back via a fresh handle.
	rf, err := cli.OpenFile(ctx, "rw.bin", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile read-back: %v", err)
	}
	defer func() { _ = rf.Close() }()
	buf := make([]byte, 16)
	got := 0
	for got < 7 {
		n, err := rf.Read(buf[got:])
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("read-back Read: %v", err)
		}
		got += n
		if errors.Is(err, io.EOF) {
			break
		}
	}
	if string(buf[:got]) != "hithere" {
		t.Errorf("read-back = %q, want %q", buf[:got], "hithere")
	}
}

// TestFileWrite_EmptyBuffer: Write(nil) / Write(zero-len) returns
// (0, nil) without a wire op.
func TestFileWrite_EmptyBuffer(t *testing.T) {
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
		n, err := f.Write(buf)
		if err != nil {
			t.Errorf("Write(%s): err=%v, want nil", name, err)
		}
		if n != 0 {
			t.Errorf("Write(%s): n=%d, want 0", name, n)
		}
	}
}

// TestFileWrite_Chunked: write a buffer larger than the negotiated
// maxChunk and verify total bytes written equals len(p). Exercises the
// chunk loop (each Twrite is clamped to maxChunk).
func TestFileWrite_Chunked(t *testing.T) {
	t.Parallel()
	// Use a small msize so writes definitely chunk. 8192 (minimum
	// that leaves plenty of room for negotiation) gives maxChunk ~=
	// 8168 bytes. A 32 KiB write loops ~4 times.
	const msize = 8192
	cli, cleanup := newClientServerPair(t, buildTestRoot(t),
		client.WithMsize(msize),
	)
	defer cleanup()
	ctx, cancel := fileIOTestCtx(t)
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

	payload := make([]byte, 32*1024)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	n, err := f.Write(payload)
	if err != nil {
		t.Fatalf("Write large: %v", err)
	}
	if n != len(payload) {
		t.Errorf("Write large: n=%d, want %d", n, len(payload))
	}
	// Sanity: Conn.Msize() returned the requested msize (or the
	// server's cap, whichever is smaller). The client must have
	// chunked through a maxChunk that is < len(payload).
	if cli.Msize() > uint32(len(payload)) {
		t.Fatalf("negotiated msize=%d > payload=%d; test precondition violated",
			cli.Msize(), len(payload))
	}
}

// TestFileRead_Chunked: read a file larger than maxChunk. Each Read
// call returns <= maxChunk bytes (the clamp); io.ReadAll composes
// multiple Reads and returns the full content.
func TestFileRead_Chunked(t *testing.T) {
	t.Parallel()
	const msize = 8192
	cli, cleanup := newClientServerPair(t, buildTestRoot(t),
		client.WithMsize(msize),
	)
	defer cleanup()
	ctx, cancel := fileIOTestCtx(t)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = root.Close() }()

	// First fill rw.bin with a known payload bigger than maxChunk.
	payload := make([]byte, 32*1024)
	for i := range payload {
		payload[i] = byte(i % 251)
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

	// Now read it back.
	f, err := cli.OpenFile(ctx, "rw.bin", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile read: %v", err)
	}
	defer func() { _ = f.Close() }()

	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != len(payload) {
		t.Errorf("ReadAll len=%d, want %d", len(got), len(payload))
	}
	for i := range got {
		if got[i] != payload[i] {
			t.Fatalf("mismatch at byte %d: got %d want %d", i, got[i], payload[i])
		}
	}
}

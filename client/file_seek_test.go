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

func fileSeekTestCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), 5*time.Second)
}

// TestFileSeek_Start: Seek(42, SeekStart) returns (42, nil) and
// advances the local offset to 42.
func TestFileSeek_Start(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileSeekTestCtx(t)
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

	pos, err := f.Seek(42, io.SeekStart)
	if err != nil {
		t.Fatalf("Seek(42, SeekStart): %v", err)
	}
	if pos != 42 {
		t.Errorf("Seek(42, SeekStart) = %d, want 42", pos)
	}
	// Cross-check via SeekCurrent(0).
	pos, err = f.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatalf("Seek(0, SeekCurrent): %v", err)
	}
	if pos != 42 {
		t.Errorf("SeekCurrent(0) = %d, want 42", pos)
	}
}

// TestFileSeek_Current: after Seek(10, SeekStart), Seek(5,
// SeekCurrent) returns (15, nil).
func TestFileSeek_Current(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileSeekTestCtx(t)
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

	if _, err := f.Seek(10, io.SeekStart); err != nil {
		t.Fatalf("Seek(10, SeekStart): %v", err)
	}
	pos, err := f.Seek(5, io.SeekCurrent)
	if err != nil {
		t.Fatalf("Seek(5, SeekCurrent): %v", err)
	}
	if pos != 15 {
		t.Errorf("Seek(5, SeekCurrent) = %d, want 15", pos)
	}
}

// TestFileSeek_NegativeAbsolute: Seek(-1, SeekStart) returns an error;
// the offset is NOT mutated.
func TestFileSeek_NegativeAbsolute(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileSeekTestCtx(t)
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

	// First move to a known position so we can prove the error path
	// didn't clobber it.
	if _, err := f.Seek(5, io.SeekStart); err != nil {
		t.Fatalf("Seek(5, SeekStart): %v", err)
	}
	if _, err := f.Seek(-1, io.SeekStart); err == nil {
		t.Fatal("Seek(-1, SeekStart): nil err, want negative-position error")
	} else if !strings.Contains(err.Error(), "negative") {
		t.Errorf("Seek(-1, SeekStart) err = %q, want mention of 'negative'", err)
	}
	pos, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatalf("Seek(0, SeekCurrent): %v", err)
	}
	if pos != 5 {
		t.Errorf("offset after rejected Seek = %d, want 5 (unchanged)", pos)
	}
}

// TestFileSeek_End_UnsetSize: with cachedSize == 0, Seek(0, SeekEnd)
// returns (0, nil); Seek(-5, SeekEnd) returns an error referencing
// File.Sync.
func TestFileSeek_End_UnsetSize(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileSeekTestCtx(t)
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

	pos, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		t.Errorf("Seek(0, SeekEnd) with cachedSize=0: err=%v, want nil", err)
	}
	if pos != 0 {
		t.Errorf("Seek(0, SeekEnd) with cachedSize=0: pos=%d, want 0", pos)
	}
	if _, err := f.Seek(-5, io.SeekEnd); err == nil {
		t.Error("Seek(-5, SeekEnd) with cachedSize=0: nil err, want negative-position error")
	} else if !strings.Contains(err.Error(), "File.Sync") {
		t.Errorf("Seek(-5, SeekEnd) err=%q, want mention of File.Sync", err)
	}
}

// TestFileSeek_End_WithCachedSize: with cachedSize manually poked to
// 100 via the export_test helper, Seek(-10, SeekEnd) returns (90, nil)
// and Seek(0, SeekEnd) returns (100, nil).
func TestFileSeek_End_WithCachedSize(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileSeekTestCtx(t)
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

	client.SetCachedSize(f, 100)

	pos, err := f.Seek(-10, io.SeekEnd)
	if err != nil {
		t.Fatalf("Seek(-10, SeekEnd): %v", err)
	}
	if pos != 90 {
		t.Errorf("Seek(-10, SeekEnd) = %d, want 90", pos)
	}
	pos, err = f.Seek(0, io.SeekEnd)
	if err != nil {
		t.Fatalf("Seek(0, SeekEnd): %v", err)
	}
	if pos != 100 {
		t.Errorf("Seek(0, SeekEnd) = %d, want 100", pos)
	}
}

// TestFileSeek_InvalidWhence: Seek(0, 42) returns an error matching
// "invalid whence".
func TestFileSeek_InvalidWhence(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileSeekTestCtx(t)
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

	if _, err := f.Seek(0, 42); err == nil {
		t.Fatal("Seek with invalid whence: nil err, want error")
	} else if !strings.Contains(err.Error(), "invalid whence") {
		t.Errorf("Seek invalid whence: err=%q, want mention of 'invalid whence'", err)
	}
}

// TestFileSeek_PastEOF_SucceedsButReadFails: Seek(1_000_000, SeekStart)
// succeeds; a subsequent Read against a small file returns
// (0, io.EOF). Matches D-11 and os.File semantics for regular files.
func TestFileSeek_PastEOF_SucceedsButReadFails(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileSeekTestCtx(t)
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

	pos, err := f.Seek(1_000_000, io.SeekStart)
	if err != nil {
		t.Fatalf("Seek past EOF: %v", err)
	}
	if pos != 1_000_000 {
		t.Errorf("Seek past EOF = %d, want 1000000", pos)
	}
	buf := make([]byte, 8)
	n, err := f.Read(buf)
	if n != 0 {
		t.Errorf("Read past EOF n=%d, want 0", n)
	}
	if !errors.Is(err, io.EOF) {
		t.Errorf("Read past EOF err=%v, want io.EOF", err)
	}
}

// TestFileSeek_Directory_Succeeds: Seek on a directory fid succeeds
// with pure local arithmetic. A subsequent Read on the directory fid
// returns either io.EOF or a *client.Error -- the invariant under
// test is that Seek itself does not reject a directory fid (D-11).
func TestFileSeek_Directory_Succeeds(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileSeekTestCtx(t)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = root.Close() }()

	// Seek on the root directory fid.
	pos, err := root.Seek(32, io.SeekStart)
	if err != nil {
		t.Fatalf("Seek on directory: %v", err)
	}
	if pos != 32 {
		t.Errorf("Seek on directory = %d, want 32", pos)
	}
}

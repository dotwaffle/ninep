package client_test

import (
	"io"
	"os"
	"testing"

	"github.com/dotwaffle/ninep/client"
)

// TestFileSync_Stub_Success: File.Sync on any *File returns nil in
// Phase 20. This documents the stub contract -- Phase 21 replaces the
// body with a Tgetattr (.L) / Tstat (.u) round-trip that populates
// cachedSize.
func TestFileSync_Stub_Success(t *testing.T) {
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

	if err := f.Sync(); err != nil {
		t.Errorf("Sync(): %v, want nil", err)
	}
	// Call again -- stub is a pure no-op; repeat must not error.
	if err := f.Sync(); err != nil {
		t.Errorf("Sync() (second call): %v, want nil", err)
	}
}

// TestFileSync_DoesNotIssueTgetattr: the Phase 20 Sync stub must NOT
// touch the wire. Measured via InflightLen stability around the call
// -- no in-flight request should register.
//
// This is a behavioral assertion; the stronger compile-time guard is
// the grep-LV assertion that "Tgetattr" does not appear in
// client/sync_stub.go, maintained by the acceptance script.
func TestFileSync_DoesNotIssueTgetattr(t *testing.T) {
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

	before := client.InflightLen(cli)
	if err := f.Sync(); err != nil {
		t.Fatalf("Sync(): %v", err)
	}
	after := client.InflightLen(cli)
	if before != after {
		t.Errorf("InflightLen delta = %d->%d, want 0 delta (stub must not touch wire)", before, after)
	}
}

// TestFileSync_SeekEndAfterSync: after calling Sync, SeekEnd(0) still
// returns 0 because the stub does not populate cachedSize. Documents
// the "Phase 21 replaces this" contract: callers depending on SeekEnd
// must either wait for Phase 21 or use the SetCachedSize test hook.
func TestFileSync_SeekEndAfterSync(t *testing.T) {
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

	if err := f.Sync(); err != nil {
		t.Fatalf("Sync(): %v", err)
	}
	pos, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		t.Fatalf("Seek(0, SeekEnd): %v", err)
	}
	if pos != 0 {
		t.Errorf("Seek(0, SeekEnd) after stub Sync = %d, want 0 (cachedSize unpopulated until Phase 21)", pos)
	}
}

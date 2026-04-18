package client

// Internal-package dialect-gate tests for the .L-only / .u-only ops. These
// tests assemble a *Conn with a chosen dialect without running Dial — the
// dialect-gate check fires at method entry BEFORE any wire action, so no
// I/O is needed. Positive-path (real round-trip) tests live in the external
// client_test package via a mock-server pair (see roundtrip_dialect_test.go).

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
)

// newGateConn returns a *Conn with the requested dialect and a closed
// net.Pipe pair. No read goroutine is spawned: the op-method gate fires
// before the wire is touched. t.Cleanup closes the pipe.
func newGateConn(t *testing.T, d protocol) *Conn {
	t.Helper()
	cliNC, srvNC := net.Pipe()
	t.Cleanup(func() {
		_ = cliNC.Close()
		_ = srvNC.Close()
	})
	return &Conn{
		nc:       cliNC,
		dialect:  d,
		msize:    65536,
		codec:    codecL, // arbitrary — gate fires first
		tags:     newTagAllocator(8),
		inflight: newInflightMap(),
		closeCh:  make(chan struct{}),
		logger:   slog.New(slog.NewTextHandler(discardWriter{}, nil)),
	}
}

// TestClient_Lopen_NotSupportedOnU: Lopen on a .u-negotiated Conn returns
// ErrNotSupported.
func TestClient_Lopen_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	c := newGateConn(t, protocolU)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, _, err := c.Lopen(ctx, 0, 0)
	if !errors.Is(err, ErrNotSupported) {
		t.Fatalf("Lopen err = %v, want ErrNotSupported", err)
	}
}

// TestClient_Lcreate_NotSupportedOnU: Lcreate on a .u-negotiated Conn
// returns ErrNotSupported.
func TestClient_Lcreate_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	c := newGateConn(t, protocolU)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, _, err := c.Lcreate(ctx, 0, "new.txt", 0, 0o644, 0)
	if !errors.Is(err, ErrNotSupported) {
		t.Fatalf("Lcreate err = %v, want ErrNotSupported", err)
	}
}

// TestClient_Open_NotSupportedOnL: Open on a .L-negotiated Conn returns
// ErrNotSupported.
func TestClient_Open_NotSupportedOnL(t *testing.T) {
	t.Parallel()
	c := newGateConn(t, protocolL)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, _, err := c.Open(ctx, 0, 0)
	if !errors.Is(err, ErrNotSupported) {
		t.Fatalf("Open err = %v, want ErrNotSupported", err)
	}
}

// TestClient_Create_NotSupportedOnL: Create on a .L-negotiated Conn returns
// ErrNotSupported.
func TestClient_Create_NotSupportedOnL(t *testing.T) {
	t.Parallel()
	c := newGateConn(t, protocolL)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, _, err := c.CreateFid(ctx, 0, "new.txt", 0o644, 0, "")
	if !errors.Is(err, ErrNotSupported) {
		t.Fatalf("Create err = %v, want ErrNotSupported", err)
	}
}

// TestClient_Phase21_DialectGates is the belt-and-braces check that every
// Phase 21 dialect-gated method returns ErrNotSupported on the wrong
// dialect without touching the wire. Per-method gate tests elsewhere
// (advanced_xattr_test.go etc.) cover the same ground; this table
// exists to catch any new .L-only / .u-only op added in a future phase
// that forgets the requireDialect check.
//
// Method selection (per 21-06-PLAN.md Task 2):
//
//   - .L-only ops exercised on a protocolU Conn: Symlink, Readlink,
//     XattrGet/Set/List/Remove, Lock/Unlock/TryLock/GetLock, Statfs,
//     Getattr, Setattr, Link, Mknod (15 rows).
//   - .u-only op exercised on a protocolL Conn: Raw.Tstat (the sole
//     public .u-only primitive; File.Stat dispatches internally).
//
// Conn.Attach / Conn.Rename / Conn.Remove are NOT in this table.
// Attach is dialect-neutral. Rename and Remove are gated to .L in
// Phase 21 execution, but their per-method tests
// (advanced_rename_test.go, advanced_remove_test.go) cover the gate;
// omitting them here keeps the "belt-and-braces" scope aligned with
// the plan's enumeration.
func TestClient_Phase21_DialectGates(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// .L-only ops exercised on a protocolU Conn. The gate fires at
	// method entry via requireDialect(protocolL, ...); f.fid is never
	// dereferenced on the wrong-dialect path, so a bare &File{conn, fid}
	// is sufficient — no tree, no open, no wire I/O.
	cU := newGateConn(t, protocolU)
	fU := &File{conn: cU, fid: 1}

	lOnlyCases := []struct {
		name string
		call func() error
	}{
		{"Conn.Symlink", func() error { _, err := cU.Symlink(ctx, "/x", "target"); return err }},
		{"File.Readlink", func() error { _, err := fU.Readlink(ctx); return err }},
		{"File.XattrGet", func() error { _, err := fU.XattrGet(ctx, "n"); return err }},
		{"File.XattrSet", func() error { return fU.XattrSet(ctx, "n", nil, 0) }},
		{"File.XattrList", func() error { _, err := fU.XattrList(ctx); return err }},
		{"File.XattrRemove", func() error { return fU.XattrRemove(ctx, "n") }},
		{"File.Lock", func() error { return fU.Lock(ctx, LockWrite) }},
		{"File.Unlock", func() error { return fU.Unlock(ctx) }},
		{"File.TryLock", func() error { _, err := fU.TryLock(ctx, LockWrite); return err }},
		{"File.GetLock", func() error { _, err := fU.GetLock(ctx, LockWrite); return err }},
		{"File.Statfs", func() error { _, err := fU.Statfs(ctx); return err }},
		{"File.Getattr", func() error { _, err := fU.Getattr(ctx, proto.AttrBasic); return err }},
		{"File.Setattr", func() error { return fU.Setattr(ctx, proto.SetAttr{}) }},
		{"Conn.Link", func() error { return cU.Link(ctx, "/a", "/b") }},
		{"Conn.Mknod", func() error { _, err := cU.Mknod(ctx, "/", "fifo", 0o644, 0, 0, 0); return err }},
	}
	for _, tc := range lOnlyCases {
		if err := tc.call(); !errors.Is(err, ErrNotSupported) {
			t.Errorf("%s on .u Conn: err = %v, want ErrNotSupported", tc.name, err)
		}
	}

	// .u-only op exercised on a protocolL Conn. Raw.Tstat is the sole
	// public .u-only primitive; File.Stat dispatches internally so it
	// is never directly dialect-gated.
	cL := newGateConn(t, protocolL)
	if _, err := cL.Raw().Tstat(ctx, 1); !errors.Is(err, ErrNotSupported) {
		t.Errorf("Raw.Tstat on .L Conn: err = %v, want ErrNotSupported", err)
	}
}

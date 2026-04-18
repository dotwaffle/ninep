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

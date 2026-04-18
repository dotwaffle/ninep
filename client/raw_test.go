package client_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
)

// rawTestCtx returns a 5s timeout ctx; mirrors roundTripTestCtx from
// roundtrip_test.go so Raw parity tests fail loudly on wire-hangs.
func rawTestCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), 5*time.Second)
}

// TestRaw_ReturnsNonNil: Conn.Raw() returns a non-nil *Raw value.
func TestRaw_ReturnsNonNil(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	r := cli.Raw()
	if r == nil {
		t.Fatal("Conn.Raw() returned nil")
	}
}

// TestRaw_Parity_Attach: Raw.Attach and Conn.Attach return the same QID
// against the same memfs root. Uses two independent Conns so fid=1
// allocation does not collide across attaches.
func TestRaw_Parity_Attach(t *testing.T) {
	t.Parallel()

	// High-level Conn.Attach baseline.
	cliA, cleanupA := newClientServerPair(t, buildTestRoot(t))
	defer cleanupA()
	ctxA, cancelA := rawTestCtx(t)
	defer cancelA()
	wantQID, err := cliA.Attach(ctxA, 1, "me", "")
	if err != nil {
		t.Fatalf("Conn.Attach: %v", err)
	}

	// Raw.Attach against a fresh pair.
	cliB, cleanupB := newClientServerPair(t, buildTestRoot(t))
	defer cleanupB()
	ctxB, cancelB := rawTestCtx(t)
	defer cancelB()
	gotQID, err := cliB.Raw().Attach(ctxB, 1, "me", "")
	if err != nil {
		t.Fatalf("Raw.Attach: %v", err)
	}

	if gotQID.Type != wantQID.Type {
		t.Errorf("QID.Type = %#x, want %#x", gotQID.Type, wantQID.Type)
	}
	if gotQID.Path != wantQID.Path {
		t.Errorf("QID.Path = %d, want %d", gotQID.Path, wantQID.Path)
	}
}

// TestRaw_Parity_Walk: Raw.Walk of zero-length names clones fid=1 into
// fid=2 and returns an empty []QID, matching Conn.Walk behavior.
func TestRaw_Parity_Walk(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := rawTestCtx(t)
	defer cancel()

	if _, err := cli.Raw().Attach(ctx, 1, "me", ""); err != nil {
		t.Fatalf("Raw.Attach: %v", err)
	}
	qids, err := cli.Raw().Walk(ctx, 1, 2, nil)
	if err != nil {
		t.Fatalf("Raw.Walk: %v", err)
	}
	if len(qids) != 0 {
		t.Errorf("Raw.Walk returned %d QIDs, want 0 (clone)", len(qids))
	}
}

// TestRaw_Parity_ReadWrite: attach → walk → Lopen → Write → Read round
// trip against rw.bin returns the bytes just written.
func TestRaw_Parity_ReadWrite(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := rawTestCtx(t)
	defer cancel()

	r := cli.Raw()

	if _, err := r.Attach(ctx, 1, "me", ""); err != nil {
		t.Fatalf("Raw.Attach: %v", err)
	}
	if _, err := r.Walk(ctx, 1, 2, []string{"rw.bin"}); err != nil {
		t.Fatalf("Raw.Walk: %v", err)
	}
	// O_RDWR = 2 on Linux.
	if _, _, err := r.Lopen(ctx, 2, 2); err != nil {
		t.Fatalf("Raw.Lopen: %v", err)
	}
	n, err := r.Write(ctx, 2, 0, []byte("hi"))
	if err != nil {
		t.Fatalf("Raw.Write: %v", err)
	}
	if n != 2 {
		t.Fatalf("Raw.Write returned n=%d, want 2", n)
	}
	data, err := r.Read(ctx, 2, 0, 2)
	if err != nil {
		t.Fatalf("Raw.Read: %v", err)
	}
	if string(data) != "hi" {
		t.Errorf("Raw.Read = %q, want %q", string(data), "hi")
	}
}

// TestRaw_Parity_Clunk: Raw.Clunk releases the server-side fid binding;
// a subsequent Raw.Read on the same fid number surfaces a server error.
func TestRaw_Parity_Clunk(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := rawTestCtx(t)
	defer cancel()

	r := cli.Raw()

	if _, err := r.Attach(ctx, 1, "me", ""); err != nil {
		t.Fatalf("Raw.Attach: %v", err)
	}
	if _, err := r.Walk(ctx, 1, 2, []string{"hello.txt"}); err != nil {
		t.Fatalf("Raw.Walk: %v", err)
	}
	if _, _, err := r.Lopen(ctx, 2, 0); err != nil {
		t.Fatalf("Raw.Lopen: %v", err)
	}
	if err := r.Clunk(ctx, 2); err != nil {
		t.Fatalf("Raw.Clunk: %v", err)
	}

	// Server should reject reads against the now-unbound fid=2.
	_, err := r.Read(ctx, 2, 0, 16)
	if err == nil {
		t.Fatal("Raw.Read after Clunk: expected error, got nil")
	}
	var cerr *client.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("Raw.Read after Clunk: err type = %T (%v), want *client.Error",
			err, err)
	}
}

// TestRaw_DialectGate_Lopen: Raw.Lopen on a .u-negotiated Conn returns
// ErrNotSupported (gate fires inside the delegated Conn.Lopen).
func TestRaw_DialectGate_Lopen(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()

	ctx, cancel := rawTestCtx(t)
	defer cancel()

	_, _, err := cli.Raw().Lopen(ctx, 1, 0)
	if !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Raw.Lopen on .u Conn: err = %v, want ErrNotSupported", err)
	}
}

// TestRaw_DialectGate_Open: Raw.Open on a .L-negotiated Conn returns
// ErrNotSupported.
func TestRaw_DialectGate_Open(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := rawTestCtx(t)
	defer cancel()

	_, _, err := cli.Raw().Open(ctx, 1, 0)
	if !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Raw.Open on .L Conn: err = %v, want ErrNotSupported", err)
	}
}

// TestRaw_Flush: Raw.Flush returns nil even for an unknown oldTag; per
// the 9P spec the server always replies Rflush.
func TestRaw_Flush(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := rawTestCtx(t)
	defer cancel()

	// Use a tag that is not currently inflight. Server must still reply
	// with Rflush per spec.
	if err := cli.Raw().Flush(ctx, proto.Tag(12345)); err != nil {
		t.Fatalf("Raw.Flush: %v", err)
	}
}

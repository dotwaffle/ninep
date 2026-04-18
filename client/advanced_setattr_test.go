package client_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
)

// TestClient_Setattr_Mode: on .L, Setattr(SetAttrMode, 0o600) followed by
// Getattr returns Mode == 0o600. Exercises the chmod path.
func TestClient_Setattr_Mode(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = root.Close() }()

	f, err := cli.OpenFile(ctx, "rw.bin", 0, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	if err := f.Setattr(ctx, proto.SetAttr{Valid: proto.SetAttrMode, Mode: 0o600}); err != nil {
		t.Fatalf("Setattr(Mode=0o600): %v", err)
	}
	attr, err := f.Getattr(ctx, proto.AttrMode)
	if err != nil {
		t.Fatalf("Getattr: %v", err)
	}
	if got := attr.Mode & 0o777; got != 0o600 {
		t.Errorf("Mode = %#o, want 0o600 (full Mode=%#o)", got, attr.Mode)
	}
}

// TestClient_Setattr_Size_Truncate: 9P2000.L truncate-via-Setattr. Writes
// "hello" (5 bytes) into rw.bin, then Setattr(SetAttrSize, 3); Getattr
// reports Size == 3.
func TestClient_Setattr_Size_Truncate(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = root.Close() }()

	// Open RW and write some data.
	f, err := cli.OpenFile(ctx, "rw.bin", 2 /*ORDWR*/, 0)
	if err != nil {
		t.Fatalf("OpenFile rw: %v", err)
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if err := f.Setattr(ctx, proto.SetAttr{Valid: proto.SetAttrSize, Size: 3}); err != nil {
		t.Fatalf("Setattr(Size=3): %v", err)
	}
	attr, err := f.Getattr(ctx, proto.AttrSize)
	if err != nil {
		t.Fatalf("Getattr: %v", err)
	}
	if attr.Size != 3 {
		t.Errorf("Size = %d, want 3", attr.Size)
	}
}

// TestClient_Setattr_NotSupportedOnU: .L-only gate.
func TestClient_Setattr_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockStatClientPair(t, wantStat)
	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	f := client.NewFileForTest(cli)
	err := f.Setattr(ctx, proto.SetAttr{Valid: proto.SetAttrMode, Mode: 0o600})
	if !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Setattr err = %v, want ErrNotSupported", err)
	}
}

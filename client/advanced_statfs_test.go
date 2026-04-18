package client_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

// TestClient_Statfs: .L round-trip against server.StaticStatFS. Asserts
// every non-zero field of the canned FSStat survives the wire.
func TestClient_Statfs(t *testing.T) {
	t.Parallel()
	want := proto.FSStat{
		Type:    0x01021997,
		BSize:   4096,
		Blocks:  100,
		BFree:   50,
		BAvail:  40,
		Files:   10,
		FFree:   5,
		FSID:    42,
		NameLen: 255,
	}
	gen := &server.QIDGenerator{}
	root := server.StaticStatFS(gen, want)

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	rootF, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = rootF.Close() }()

	got, err := rootF.Statfs(ctx)
	if err != nil {
		t.Fatalf("Statfs: %v", err)
	}
	if got != want {
		t.Errorf("Statfs = %+v\nwant  %+v", got, want)
	}
}

// TestClient_Statfs_NotSupportedOnU: .L-only gate.
func TestClient_Statfs_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockStatClientPair(t, wantStat)
	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	f := client.NewFileForTest(cli)
	_, err := f.Statfs(ctx)
	if !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Statfs err = %v, want ErrNotSupported", err)
	}
}

// TestClient_Statfs_ReturnsByValue: compile-time + runtime evidence that
// File.Statfs returns proto.FSStat by VALUE, not *proto.FSStat (Pitfall
// 8). The `var _ proto.FSStat = fs` line below would refuse to compile
// if the signature ever regressed to a pointer return. Runtime check
// verifies the value is populated (not zero-value) in the happy path.
func TestClient_Statfs_ReturnsByValue(t *testing.T) {
	t.Parallel()
	want := proto.FSStat{Type: 0xdeadbeef, BSize: 8192, Blocks: 1}
	gen := &server.QIDGenerator{}
	root := server.StaticStatFS(gen, want)

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	rootF, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = rootF.Close() }()

	// Compile-time assertion: the declared type on the LHS must match the
	// method's actual return type. If Statfs ever regressed to a pointer
	// return, this line would refuse to compile.
	var fs proto.FSStat
	fs, err = rootF.Statfs(ctx)
	if err != nil {
		t.Fatalf("Statfs: %v", err)
	}
	if fs == (proto.FSStat{}) {
		t.Errorf("Statfs returned zero-value, want populated struct")
	}
	if fs.Type != want.Type {
		t.Errorf("Type = %#x, want %#x", fs.Type, want.Type)
	}
}

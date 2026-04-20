package client_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
	"github.com/dotwaffle/ninep/server/memfs"
)

func TestDial_WithVersion_Enforcement(t *testing.T) {
	t.Parallel()

	// Setup a server that only speaks .u
	gen := new(server.QIDGenerator)
	root := memfs.NewDir(gen)
	srv := server.New(root)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()

	go func() {
		_ = srv.Serve(context.Background(), l)
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	// 1. Dial with default (should succeed and negotiate .L if server supports it,
	//    but our server currently supports both and defaults to .L if proposed).
	nc1, _ := net.Dial("tcp", l.Addr().String())
	c1, err := client.Dial(ctx, nc1)
	if err != nil {
		t.Fatalf("Default Dial failed: %v", err)
	}
	_ = c1.Close()

	// 2. Dial with explicit .u (should succeed)
	nc2, _ := net.Dial("tcp", l.Addr().String())
	c2, err := client.Dial(ctx, nc2, client.WithVersion(proto.VersionU))
	if err != nil {
		t.Fatalf("Dial WithVersion(.u) failed: %v", err)
	}
	_ = c2.Close()

	// 3. Dial with explicit .L (should succeed)
	nc3, _ := net.Dial("tcp", l.Addr().String())
	c3, err := client.Dial(ctx, nc3, client.WithVersion(proto.VersionL))
	if err != nil {
		t.Fatalf("Dial WithVersion(.L) failed: %v", err)
	}
	_ = c3.Close()

	// 4. Dial with unknown version (should fail)
	nc4, _ := net.Dial("tcp", l.Addr().String())
	_, err = client.Dial(ctx, nc4, client.WithVersion("9P2000.unknown"))
	if !errors.Is(err, client.ErrVersionMismatch) {
		t.Errorf("Dial unknown: err = %v, want ErrVersionMismatch", err)
	}
}

package client_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9u"
)

func TestClient_Ergo_Chmod(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newClientServerPair(t, buildTestRoot(t))

	if _, err := c.Attach(ctx, "nobody", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	f, err := c.OpenFile(ctx, "rw.bin", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	if err := f.Chmod(ctx, 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	attr, err := f.Getattr(ctx, proto.AttrMode)
	if err != nil {
		t.Fatalf("Getattr: %v", err)
	}
	if attr.Mode&0o777 != 0o600 {
		t.Errorf("Mode = %o, want %o", attr.Mode&0o777, 0o600)
	}
}

func TestClient_Ergo_Chown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newClientServerPair(t, buildTestRoot(t))

	if _, err := c.Attach(ctx, "nobody", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	f, err := c.OpenFile(ctx, "rw.bin", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	if err := f.Chown(ctx, 1000, 1001); err != nil {
		t.Fatalf("Chown: %v", err)
	}

	attr, err := f.Getattr(ctx, proto.AttrUID|proto.AttrGID)
	if err != nil {
		t.Fatalf("Getattr: %v", err)
	}
	if attr.UID != 1000 || attr.GID != 1001 {
		t.Errorf("UID:GID = %d:%d, want 1000:1001", attr.UID, attr.GID)
	}
}

func TestClient_Ergo_Truncate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newClientServerPair(t, buildTestRoot(t))

	if _, err := c.Attach(ctx, "nobody", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	f, err := c.OpenFile(ctx, "rw.bin", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	if err := f.Truncate(ctx, 123); err != nil {
		t.Fatalf("Truncate: %v", err)
	}

	attr, err := f.Getattr(ctx, proto.AttrSize)
	if err != nil {
		t.Fatalf("Getattr: %v", err)
	}
	if attr.Size != 123 {
		t.Errorf("Size = %d, want 123", attr.Size)
	}
}

func TestClient_Ergo_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newUMockStatClientPair(t, p9u.Stat{})

	f := client.NewFileForTest(c)
	if err := f.Chmod(ctx, 0o644); !errors.Is(err, client.ErrNotSupported) {
		t.Errorf("Chmod err = %v, want ErrNotSupported", err)
	}
	if err := f.Chown(ctx, 1, 1); !errors.Is(err, client.ErrNotSupported) {
		t.Errorf("Chown err = %v, want ErrNotSupported", err)
	}
	if err := f.Truncate(ctx, 0); !errors.Is(err, client.ErrNotSupported) {
		t.Errorf("Truncate err = %v, want ErrNotSupported", err)
	}
}

package client_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
)

// roundTripTestCtx returns a 5s timeout ctx; all integration tests use this
// to fail loudly rather than hang if a wire-level bug causes the op method
// to block forever.
func roundTripTestCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), 5*time.Second)
}

// TestClient_Attach_RoundTrip: Attach(fid=0, uname, aname) returns the root
// QID which has QTDIR type.
func TestClient_Attach_RoundTrip(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := roundTripTestCtx(t)
	defer cancel()

	qid, err := cli.Attach(ctx, 0, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if qid.Type&proto.QTDIR == 0 {
		t.Errorf("root QID type = %#x, want QTDIR bit set", qid.Type)
	}
}

// TestClient_Walk_EmptyNames: walk with zero names clones the fid.
func TestClient_Walk_EmptyNames(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := roundTripTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, 0, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	qids, err := cli.Walk(ctx, 0, 1, nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(qids) != 0 {
		t.Errorf("Walk [] returned %d QIDs, want 0", len(qids))
	}
}

// TestClient_Walk_NNames: walk to an existing leaf returns one QID per name.
func TestClient_Walk_NNames(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := roundTripTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, 0, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	qids, err := cli.Walk(ctx, 0, 1, []string{"hello.txt"})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(qids) != 1 {
		t.Fatalf("Walk returned %d QIDs, want 1", len(qids))
	}
	if qids[0].Type&proto.QTDIR != 0 {
		t.Errorf("hello.txt QID type = %#x, want non-directory", qids[0].Type)
	}
}

// TestClient_Walk_NonexistentPath: walk to a missing name returns a
// *client.Error with Errno=ENOENT.
func TestClient_Walk_NonexistentPath(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := roundTripTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, 0, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	_, err := cli.Walk(ctx, 0, 1, []string{"does-not-exist"})
	if err == nil {
		t.Fatal("Walk: expected error, got nil")
	}
	if !errors.Is(err, proto.ENOENT) {
		t.Errorf("Walk err = %v, want errors.Is(proto.ENOENT)", err)
	}
	var ce *client.Error
	if !errors.As(err, &ce) {
		t.Errorf("Walk err = %T, want *client.Error", err)
	}
}

// TestClient_Read_StaticFile: read the contents of a static file.
func TestClient_Read_StaticFile(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := roundTripTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, 0, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if _, err := cli.Walk(ctx, 0, 1, []string{"hello.txt"}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if _, _, err := cli.Lopen(ctx, 1, 0); err != nil { // O_RDONLY == 0
		t.Fatalf("Lopen: %v", err)
	}
	data, err := cli.Read(ctx, 1, 0, 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(data) != "hello world\n" {
		t.Errorf("Read data = %q, want %q", string(data), "hello world\n")
	}
}

// TestClient_Write_RwFile: write to a read-write backed file.
func TestClient_Write_RwFile(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := roundTripTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, 0, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if _, err := cli.Walk(ctx, 0, 1, []string{"rw.bin"}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if _, _, err := cli.Lopen(ctx, 1, 2); err != nil { // O_RDWR == 2
		t.Fatalf("Lopen: %v", err)
	}
	payload := []byte("data")
	n, err := cli.Write(ctx, 1, 0, payload)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != uint32(len(payload)) {
		t.Errorf("Write returned %d, want %d", n, len(payload))
	}
}

// TestClient_Clunk_Success: clunk a fid; subsequent op on that fid returns
// a server error.
func TestClient_Clunk_Success(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := roundTripTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, 0, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := cli.Clunk(ctx, 0); err != nil {
		t.Fatalf("Clunk: %v", err)
	}

	// Subsequent walk on clunked fid should fail with a server-reported error.
	_, err := cli.Walk(ctx, 0, 1, nil)
	if err == nil {
		t.Fatal("Walk on clunked fid: expected error, got nil")
	}
}

// TestClient_Flush_NoMatch: Flush on an unknown oldTag returns nil per
// 9P spec (the server always responds with Rflush).
func TestClient_Flush_NoMatch(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := roundTripTestCtx(t)
	defer cancel()

	if err := cli.Flush(ctx, proto.Tag(999)); err != nil {
		t.Fatalf("Flush: %v", err)
	}
}

// TestClient_Read_EmptyFile: reading an empty static file returns empty.
func TestClient_Read_EmptyFile(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := roundTripTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, 0, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if _, err := cli.Walk(ctx, 0, 1, []string{"empty.txt"}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if _, _, err := cli.Lopen(ctx, 1, 0); err != nil {
		t.Fatalf("Lopen: %v", err)
	}
	data, err := cli.Read(ctx, 1, 0, 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("Read returned %d bytes, want 0", len(data))
	}
}

// TestClient_Lopen_L: Lopen against a .L-negotiated Conn returns the file's
// QID and a server-chosen iounit.
func TestClient_Lopen_L(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := roundTripTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, 0, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if _, err := cli.Walk(ctx, 0, 1, []string{"hello.txt"}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	qid, _, err := cli.Lopen(ctx, 1, 0) // O_RDONLY
	if err != nil {
		t.Fatalf("Lopen: %v", err)
	}
	if qid.Type&proto.QTDIR != 0 {
		t.Errorf("hello.txt QID type = %#x, want non-directory", qid.Type)
	}
}

// TestClient_Lcreate_L: create a new file in the root dir over a .L Conn.
// memfs.MemDir implements NodeCreater (server calls the same entry point
// for both Tcreate and Tlcreate via bridge.handleLcreate).
func TestClient_Lcreate_L(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := roundTripTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, 0, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	// Clone root into fid 1 — Lcreate mutates the supplied fid into the
	// newly-created file per the 9P spec, so we must not burn the root.
	if _, err := cli.Walk(ctx, 0, 1, nil); err != nil {
		t.Fatalf("Walk-clone: %v", err)
	}
	// Lcreate: O_RDWR=2, mode=0o644, gid=0.
	qid, _, err := cli.Lcreate(ctx, 1, "new.txt", 2, proto.FileMode(0o644), 0)
	if err != nil {
		t.Fatalf("Lcreate: %v", err)
	}
	if qid.Type&proto.QTDIR != 0 {
		t.Errorf("new.txt QID type = %#x, want file", qid.Type)
	}
	// Fid 1 is now open on the new file; read should return 0 bytes (empty).
	data, err := cli.Read(ctx, 1, 0, 100)
	if err != nil {
		t.Fatalf("Read after Lcreate: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("Read on freshly-created file: %d bytes, want 0", len(data))
	}
}

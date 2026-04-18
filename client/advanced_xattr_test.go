package client_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
	"github.com/dotwaffle/ninep/server/memfs"
)

// xattrCtx returns a 5s timeout ctx; mirrors pair_test.go timing for
// consistency with the rest of the client test suite.
func xattrCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), 5*time.Second)
}

// xattrRoot wires a memfs dir root containing a single testXattrNode at
// the given name. The returned *testXattrNode is the fixture the caller
// mutates for assertions; the returned Node is the server root passed
// to newClientServerPair.
func xattrRoot(t *testing.T, childName string, seed map[string][]byte) (server.Node, *testXattrNode) {
	t.Helper()
	gen := &server.QIDGenerator{}
	root := memfs.NewDir(gen)
	x := newTestXattrNode(gen, seed)
	root.AddChild(childName, x.EmbeddedInode())
	return root, x
}

// fileForConn returns a synthetic *File bound to cli for dialect-gate
// tests. The gate fires at the ops entry before any wire op, so the
// File's fid does not need to be server-valid.
func fileForConn(t *testing.T, cli *client.Conn) *client.File {
	t.Helper()
	return client.NewFileForTest(cli)
}

// walkToXattrFile attaches the connection and walks from the root to the
// xattr-bearing child. Returns the walked-to *File (unopened -- xattr ops
// do not require Lopen).
func walkToXattrFile(t *testing.T, cli *client.Conn, childName string) *client.File {
	t.Helper()
	ctx, cancel := xattrCtx(t)
	defer cancel()
	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	f, err := root.Walk(ctx, []string{childName})
	if err != nil {
		t.Fatalf("Walk %q: %v", childName, err)
	}
	return f
}

// -- XattrGet --------------------------------------------------------

func TestClient_XattrGet_Happy(t *testing.T) {
	t.Parallel()
	root, _ := xattrRoot(t, "x", map[string][]byte{
		"user.comment": []byte("hello"),
	})
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	f := walkToXattrFile(t, cli, "x")
	defer func() { _ = f.Close() }()

	ctx, cancel := xattrCtx(t)
	defer cancel()
	got, err := f.XattrGet(ctx, "user.comment")
	if err != nil {
		t.Fatalf("XattrGet: %v", err)
	}
	if !bytes.Equal(got, []byte("hello")) {
		t.Errorf("XattrGet = %q, want %q", got, "hello")
	}
}

func TestClient_XattrGet_EmptyValue(t *testing.T) {
	t.Parallel()
	root, _ := xattrRoot(t, "x", map[string][]byte{
		"user.empty": {},
	})
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	f := walkToXattrFile(t, cli, "x")
	defer func() { _ = f.Close() }()

	ctx, cancel := xattrCtx(t)
	defer cancel()
	got, err := f.XattrGet(ctx, "user.empty")
	if err != nil {
		t.Fatalf("XattrGet: %v", err)
	}
	if got == nil {
		t.Fatal("XattrGet returned nil slice, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("XattrGet len = %d, want 0", len(got))
	}
}

func TestClient_XattrGet_NotFound(t *testing.T) {
	t.Parallel()
	root, _ := xattrRoot(t, "x", map[string][]byte{})
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	f := walkToXattrFile(t, cli, "x")
	defer func() { _ = f.Close() }()

	ctx, cancel := xattrCtx(t)
	defer cancel()
	_, err := f.XattrGet(ctx, "user.absent")
	if err == nil {
		t.Fatal("XattrGet on missing attr: want error, got nil")
	}
	var cerr *client.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("XattrGet err = %v (%T), want *client.Error", err, err)
	}
	if !errors.Is(err, proto.ENODATA) {
		t.Errorf("XattrGet errno = %v, want ENODATA", cerr.Errno)
	}
}

func TestClient_XattrGet_Large(t *testing.T) {
	t.Parallel()
	// 1 MiB value -- exercises the multi-Tread chunk loop. The default
	// msize (65536) bounds each Tread to ~msize-24 bytes, so this takes
	// ~16 Tread round-trips.
	const size = 1 << 20
	want := make([]byte, size)
	for i := range want {
		want[i] = byte(i)
	}
	root, _ := xattrRoot(t, "x", map[string][]byte{"user.big": want})
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	f := walkToXattrFile(t, cli, "x")
	defer func() { _ = f.Close() }()

	ctx, cancel := xattrCtx(t)
	defer cancel()
	got, err := f.XattrGet(ctx, "user.big")
	if err != nil {
		t.Fatalf("XattrGet: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("XattrGet mismatch: len=%d want len=%d", len(got), len(want))
	}
}

// TestClient_XattrGet_NotSupportedOnU: .u-negotiated Conn rejects
// XattrGet at the requireDialect gate before any wire op.
func TestClient_XattrGet_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()

	f := fileForConn(t, cli)
	ctx, cancel := xattrCtx(t)
	defer cancel()
	if _, err := f.XattrGet(ctx, "user.x"); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("XattrGet err = %v, want ErrNotSupported", err)
	}
}

// -- XattrSet --------------------------------------------------------

func TestClient_XattrSet_Happy(t *testing.T) {
	t.Parallel()
	root, backing := xattrRoot(t, "x", map[string][]byte{})
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	f := walkToXattrFile(t, cli, "x")
	defer func() { _ = f.Close() }()

	ctx, cancel := xattrCtx(t)
	defer cancel()
	if err := f.XattrSet(ctx, "user.comment", []byte("world"), 0); err != nil {
		t.Fatalf("XattrSet: %v", err)
	}
	backing.mu.Lock()
	got, ok := backing.xattrs["user.comment"]
	backing.mu.Unlock()
	if !ok {
		t.Fatal("server did not receive xattr")
	}
	if string(got) != "world" {
		t.Errorf("server-side value = %q, want %q", got, "world")
	}
	got2, err := f.XattrGet(ctx, "user.comment")
	if err != nil {
		t.Fatalf("XattrGet after XattrSet: %v", err)
	}
	if string(got2) != "world" {
		t.Errorf("XattrGet = %q, want %q", got2, "world")
	}
}

// TestClient_XattrSet_CloneIsolation proves Pitfall 1: the caller's
// *File f is NOT invalidated by XattrSet. Two consecutive XattrSets on
// the same *File must both succeed (each Set clones internally).
func TestClient_XattrSet_CloneIsolation(t *testing.T) {
	t.Parallel()
	root, _ := xattrRoot(t, "x", map[string][]byte{})
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	f := walkToXattrFile(t, cli, "x")
	defer func() { _ = f.Close() }()

	ctx, cancel := xattrCtx(t)
	defer cancel()
	if err := f.XattrSet(ctx, "user.a", []byte("1"), 0); err != nil {
		t.Fatalf("first XattrSet: %v", err)
	}
	if err := f.XattrSet(ctx, "user.b", []byte("2"), 0); err != nil {
		t.Fatalf("second XattrSet (caller's *File must still be valid): %v", err)
	}
	got, err := f.XattrGet(ctx, "user.a")
	if err != nil {
		t.Fatalf("XattrGet after two XattrSets: %v", err)
	}
	if string(got) != "1" {
		t.Errorf("XattrGet(user.a) = %q, want %q", got, "1")
	}
}

func TestClient_XattrSet_LargeValue(t *testing.T) {
	t.Parallel()
	root, backing := xattrRoot(t, "x", map[string][]byte{})
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	f := walkToXattrFile(t, cli, "x")
	defer func() { _ = f.Close() }()

	// Server clamps Txattrcreate.AttrSize to msize (server/bridge.go:897),
	// so pick slightly under the negotiated 65536-byte msize to exercise
	// multi-Twrite chunking without tripping the server-side clamp.
	const size = 60000
	value := make([]byte, size)
	for i := range value {
		value[i] = byte(i * 7)
	}
	ctx, cancel := xattrCtx(t)
	defer cancel()
	if err := f.XattrSet(ctx, "user.big", value, 0); err != nil {
		t.Fatalf("XattrSet (size=%d): %v", size, err)
	}
	backing.mu.Lock()
	got := append([]byte(nil), backing.xattrs["user.big"]...)
	backing.mu.Unlock()
	if !bytes.Equal(got, value) {
		t.Errorf("server-side xattr mismatch: got len=%d want len=%d", len(got), len(value))
	}
}

func TestClient_XattrSet_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()
	f := fileForConn(t, cli)
	ctx, cancel := xattrCtx(t)
	defer cancel()
	if err := f.XattrSet(ctx, "user.x", []byte{1}, 0); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("XattrSet err = %v, want ErrNotSupported", err)
	}
}

// -- XattrList -------------------------------------------------------

func TestClient_XattrList_Happy(t *testing.T) {
	t.Parallel()
	root, _ := xattrRoot(t, "x", map[string][]byte{
		"user.a": []byte("1"),
		"user.b": []byte("2"),
		"user.c": []byte("3"),
	})
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	f := walkToXattrFile(t, cli, "x")
	defer func() { _ = f.Close() }()

	ctx, cancel := xattrCtx(t)
	defer cancel()
	names, err := f.XattrList(ctx)
	if err != nil {
		t.Fatalf("XattrList: %v", err)
	}
	want := []string{"user.a", "user.b", "user.c"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("XattrList = %v, want %v", names, want)
	}
}

func TestClient_XattrList_Empty(t *testing.T) {
	t.Parallel()
	root, _ := xattrRoot(t, "x", map[string][]byte{})
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	f := walkToXattrFile(t, cli, "x")
	defer func() { _ = f.Close() }()

	ctx, cancel := xattrCtx(t)
	defer cancel()
	names, err := f.XattrList(ctx)
	if err != nil {
		t.Fatalf("XattrList: %v", err)
	}
	if names == nil {
		t.Fatal("XattrList returned nil slice, want non-nil empty slice")
	}
	if len(names) != 0 {
		t.Errorf("XattrList len = %d, want 0 (got %v)", len(names), names)
	}
}

// TestClient_XattrList_TrailingNul: server encodes the xattr list as
// "name1\x00name2\x00...\x00" (each name NUL-terminated). A naive
// Split(..., "\x00") yields a trailing empty string; XattrList must
// drop it.
func TestClient_XattrList_TrailingNul(t *testing.T) {
	t.Parallel()
	root, _ := xattrRoot(t, "x", map[string][]byte{
		"only": []byte("v"),
	})
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	f := walkToXattrFile(t, cli, "x")
	defer func() { _ = f.Close() }()

	ctx, cancel := xattrCtx(t)
	defer cancel()
	names, err := f.XattrList(ctx)
	if err != nil {
		t.Fatalf("XattrList: %v", err)
	}
	if len(names) != 1 || names[0] != "only" {
		t.Errorf("XattrList = %v, want [\"only\"] (no trailing empty)", names)
	}
}

func TestClient_XattrList_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()
	f := fileForConn(t, cli)
	ctx, cancel := xattrCtx(t)
	defer cancel()
	if _, err := f.XattrList(ctx); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("XattrList err = %v, want ErrNotSupported", err)
	}
}

// -- XattrRemove -----------------------------------------------------

func TestClient_XattrRemove_Happy(t *testing.T) {
	t.Parallel()
	root, backing := xattrRoot(t, "x", map[string][]byte{
		"user.a": []byte("1"),
	})
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	f := walkToXattrFile(t, cli, "x")
	defer func() { _ = f.Close() }()

	ctx, cancel := xattrCtx(t)
	defer cancel()
	if err := f.XattrRemove(ctx, "user.a"); err != nil {
		t.Fatalf("XattrRemove: %v", err)
	}
	backing.mu.Lock()
	_, stillThere := backing.xattrs["user.a"]
	backing.mu.Unlock()
	if stillThere {
		t.Error("server-side xattr still present after XattrRemove")
	}
	_, err := f.XattrGet(ctx, "user.a")
	if err == nil {
		t.Fatal("XattrGet after XattrRemove: want ENODATA, got nil")
	}
	if !errors.Is(err, proto.ENODATA) {
		t.Errorf("XattrGet err = %v, want ENODATA", err)
	}
}

func TestClient_XattrRemove_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()
	f := fileForConn(t, cli)
	ctx, cancel := xattrCtx(t)
	defer cancel()
	if err := f.XattrRemove(ctx, "user.x"); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("XattrRemove err = %v, want ErrNotSupported", err)
	}
}

// -- Fid-leak stress -------------------------------------------------

// TestClient_Xattr_NoFidLeak: 250 iterations of Set/Get/List against
// the same *File. Each XattrSet spends one Clone-fid and releases it;
// the allocator's 1024-slot reuse cache must absorb the traffic.
// Asserts the reuse cache is populated at the end (i.e. fids return).
func TestClient_Xattr_NoFidLeak(t *testing.T) {
	t.Parallel()
	root, _ := xattrRoot(t, "x", map[string][]byte{})
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	f := walkToXattrFile(t, cli, "x")
	defer func() { _ = f.Close() }()

	ctx, cancel := xattrCtx(t)
	defer cancel()

	const iters = 250
	for i := 0; i < iters; i++ {
		if err := f.XattrSet(ctx, "user.iter", []byte{byte(i)}, 0); err != nil {
			t.Fatalf("iter %d: XattrSet: %v", i, err)
		}
		if _, err := f.XattrGet(ctx, "user.iter"); err != nil {
			t.Fatalf("iter %d: XattrGet: %v", i, err)
		}
		if _, err := f.XattrList(ctx); err != nil {
			t.Fatalf("iter %d: XattrList: %v", i, err)
		}
	}
	if depth := client.FidReuseLen(cli); depth == 0 {
		t.Errorf("fid reuse cache depth = 0 after %d iters; suspects: XattrSet clone leak or XattrGet newFid leak", iters)
	}
}

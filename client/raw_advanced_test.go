package client_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
	"github.com/dotwaffle/ninep/server/memfs"
)

// -- inline test fixtures for Wave-1 Raw round-trips -----------------
//
// These fixtures are deliberately minimal: just enough surface to
// exercise each Raw.T<op> wire primitive. Wave-2 plans (21-02..21-04)
// ship their own richer fixtures in disjoint per-plan files
// (advanced_tree_testnodes_test.go, advanced_xattr_testnode_test.go,
// advanced_lock_testnode_test.go) so parallel execution stays safe.
// Types defined here are prefixed "raw" to avoid collisions with
// Wave-2 types in the same package when the diffs merge.

// rawTestXattrNode exposes the 4 simple xattr interfaces over a
// map-backed store. Sufficient for Raw.Txattrwalk / Raw.Txattrcreate
// round-trips.
type rawTestXattrNode struct {
	server.Inode
	mu      sync.Mutex
	attrs   map[string][]byte
	lastSet string
}

func (n *rawTestXattrNode) GetXattr(_ context.Context, name string) ([]byte, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if v, ok := n.attrs[name]; ok {
		return append([]byte(nil), v...), nil
	}
	return nil, proto.ENODATA
}

func (n *rawTestXattrNode) SetXattr(_ context.Context, name string, data []byte, _ uint32) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.attrs == nil {
		n.attrs = make(map[string][]byte)
	}
	n.attrs[name] = append([]byte(nil), data...)
	n.lastSet = name
	return nil
}

func (n *rawTestXattrNode) ListXattrs(_ context.Context) ([]string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	names := make([]string, 0, len(n.attrs))
	for k := range n.attrs {
		names = append(names, k)
	}
	return names, nil
}

func (n *rawTestXattrNode) RemoveXattr(_ context.Context, name string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.attrs, name)
	return nil
}

// rawTestLockerNode implements NodeLocker. Lock() returns
// LockStatusOK unconditionally; GetLock() reports the region is free
// (LockTypeUnlck). Wave 2 plan 21-04 ships a richer fixture with a
// call-log and a programmable status queue.
type rawTestLockerNode struct {
	server.Inode
}

func (n *rawTestLockerNode) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return nil, 0, nil
}

func (n *rawTestLockerNode) Lock(_ context.Context, _ proto.LockType, _ proto.LockFlags, _, _ uint64, _ uint32, _ string) (proto.LockStatus, error) {
	return proto.LockStatusOK, nil
}

func (n *rawTestLockerNode) GetLock(_ context.Context, _ proto.LockType, start, length uint64, procID uint32, clientID string) (proto.LockType, uint64, uint64, uint32, string, error) {
	_ = procID
	_ = clientID
	return proto.LockTypeUnlck, start, length, 0, "", nil
}

// rawTestRUDir composes MemDir capabilities with Rename/Link/Mknod
// support so Raw.Trename/Trenameat/Tlink/Tmknod/Tunlinkat round-trip.
// MemDir already implements NodeUnlinker + NodeCreater + NodeMkdirer;
// we add the rest over the Inode tree.
type rawTestRUDir struct {
	server.Inode
	gen *server.QIDGenerator
	// track last symlink creation for optional assertion
	lastSymlink string
}

func (d *rawTestRUDir) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return nil, 0, nil
}

func (d *rawTestRUDir) Readdir(_ context.Context) ([]proto.Dirent, error) {
	children := d.Children()
	ents := make([]proto.Dirent, 0, len(children))
	var offset uint64
	for name, inode := range children {
		qid := inode.QID()
		offset++
		ents = append(ents, proto.Dirent{
			QID:    qid,
			Offset: offset,
			Type:   proto.QIDTypeToDT(qid.Type),
			Name:   name,
		})
	}
	return ents, nil
}

func (d *rawTestRUDir) Rename(_ context.Context, oldName string, newDir server.Node, newName string) error {
	oldInode := d.Children()[oldName]
	if oldInode == nil {
		return proto.ENOENT
	}
	newDirEmbed, ok := newDir.(server.InodeEmbedder)
	if !ok {
		return proto.ENOTSUP
	}
	d.RemoveChild(oldName)
	newDirEmbed.EmbeddedInode().AddChild(newName, oldInode)
	return nil
}

func (d *rawTestRUDir) Unlink(_ context.Context, name string, _ uint32) error {
	if _, ok := d.Children()[name]; !ok {
		return proto.ENOENT
	}
	d.RemoveChild(name)
	return nil
}

func (d *rawTestRUDir) Link(_ context.Context, target server.Node, name string) error {
	te, ok := target.(server.InodeEmbedder)
	if !ok {
		return proto.ENOTSUP
	}
	d.AddChild(name, te.EmbeddedInode())
	return nil
}

func (d *rawTestRUDir) Mknod(_ context.Context, name string, _ proto.FileMode, major, minor, _ uint32) (server.Node, error) {
	dev := server.DeviceNode(d.gen, major, minor)
	d.AddChild(name, dev.EmbeddedInode())
	return dev, nil
}

func (d *rawTestRUDir) Symlink(_ context.Context, name, target string, _ uint32) (server.Node, error) {
	sym := server.SymlinkTo(d.gen, target)
	d.AddChild(name, sym.EmbeddedInode())
	d.lastSymlink = name
	return sym, nil
}

// newRawRUDir wires a rawTestRUDir as the root with a QID generator.
func newRawRUDir(tb testing.TB) *rawTestRUDir {
	tb.Helper()
	gen := &server.QIDGenerator{}
	d := &rawTestRUDir{gen: gen}
	d.Init(gen.Next(proto.QTDIR), d)
	return d
}

// -- helpers --------------------------------------------------------

// rawAdvCtx returns a 5s timeout ctx mirroring pair_test.go:76.
func rawAdvCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), 5*time.Second)
}

// attachRoot walks Attach+clone into fid=rootFid for tests.
func attachRoot(t *testing.T, cli *client.Conn, rootFid proto.Fid) {
	t.Helper()
	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	if _, err := cli.Raw().Attach(ctx, rootFid, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
}

// walkFresh clones rootFid into newFid (zero-length Walk) for
// obtaining a working fid the caller can then open/stat.
func walkFresh(t *testing.T, cli *client.Conn, rootFid, newFid proto.Fid) {
	t.Helper()
	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	if _, err := cli.Raw().Walk(ctx, rootFid, newFid, nil); err != nil {
		t.Fatalf("Walk clone: %v", err)
	}
}

// walkTo walks rootFid along names into newFid.
func walkTo(t *testing.T, cli *client.Conn, rootFid, newFid proto.Fid, names []string) {
	t.Helper()
	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	if _, err := cli.Raw().Walk(ctx, rootFid, newFid, names); err != nil {
		t.Fatalf("Walk %v: %v", names, err)
	}
}

// -- .L round-trip tests --------------------------------------------

func TestRaw_Tstatfs_RoundTrip(t *testing.T) {
	t.Parallel()
	gen := &server.QIDGenerator{}
	want := proto.FSStat{
		Type:    0x01021997,
		BSize:   4096,
		Blocks:  100,
		NameLen: 255,
	}
	root := server.StaticStatFS(gen, want)

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	attachRoot(t, cli, 1)
	ctx, cancel := rawAdvCtx(t)
	defer cancel()

	got, err := cli.Raw().Tstatfs(ctx, 1)
	if err != nil {
		t.Fatalf("Tstatfs: %v", err)
	}
	if got.Type != want.Type || got.BSize != want.BSize || got.Blocks != want.Blocks || got.NameLen != want.NameLen {
		t.Errorf("Tstatfs = %+v, want %+v", got, want)
	}
}

func TestRaw_Tgetattr_RoundTrip(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	attachRoot(t, cli, 1)
	walkTo(t, cli, 1, 2, []string{"hello.txt"})

	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	attr, err := cli.Raw().Tgetattr(ctx, 2, proto.AttrBasic)
	if err != nil {
		t.Fatalf("Tgetattr: %v", err)
	}
	if attr.Size != uint64(len("hello world\n")) {
		t.Errorf("Tgetattr.Size = %d, want %d", attr.Size, len("hello world\n"))
	}
}

func TestRaw_Tsetattr_RoundTrip(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	attachRoot(t, cli, 1)
	walkTo(t, cli, 1, 2, []string{"rw.bin"})

	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	sa := proto.SetAttr{
		Valid: proto.SetAttrSize,
		Size:  128,
	}
	if err := cli.Raw().Tsetattr(ctx, 2, sa); err != nil {
		t.Fatalf("Tsetattr: %v", err)
	}

	attr, err := cli.Raw().Tgetattr(ctx, 2, proto.AttrBasic)
	if err != nil {
		t.Fatalf("post-Tsetattr Tgetattr: %v", err)
	}
	if attr.Size != 128 {
		t.Errorf("post-Tsetattr size = %d, want 128", attr.Size)
	}
}

func TestRaw_Tsymlink_Treadlink_RoundTrip(t *testing.T) {
	t.Parallel()
	root := newRawRUDir(t)
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	attachRoot(t, cli, 1)

	// Symlink under root.
	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	qid, err := cli.Raw().Tsymlink(ctx, 1, "link", "/target/path", 0)
	if err != nil {
		t.Fatalf("Tsymlink: %v", err)
	}
	if qid.Type&proto.QTSYMLINK == 0 {
		t.Errorf("Tsymlink QID.Type = %#x, missing QTSYMLINK bit", qid.Type)
	}
	if root.lastSymlink != "link" {
		t.Errorf("server lastSymlink = %q, want %q", root.lastSymlink, "link")
	}

	// Walk to the new symlink and readlink it.
	walkTo(t, cli, 1, 2, []string{"link"})
	got, err := cli.Raw().Treadlink(ctx, 2)
	if err != nil {
		t.Fatalf("Treadlink: %v", err)
	}
	if got != "/target/path" {
		t.Errorf("Treadlink = %q, want %q", got, "/target/path")
	}
}

func TestRaw_Txattrwalk_Txattrcreate_RoundTrip(t *testing.T) {
	t.Parallel()
	gen := &server.QIDGenerator{}
	root := memfs.NewDir(gen)

	xfile := &rawTestXattrNode{}
	xfile.Init(gen.Next(proto.QTFILE), xfile)
	root.AddChild("x", xfile.EmbeddedInode())

	// Pre-seed one attribute to exercise Txattrwalk-with-name.
	xfile.attrs = map[string][]byte{"user.existing": []byte("old-value")}

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	attachRoot(t, cli, 1)

	ctx, cancel := rawAdvCtx(t)
	defer cancel()

	// Txattrwalk "user.existing" should report 9 bytes.
	walkTo(t, cli, 1, 2, []string{"x"})
	// Fresh fid=3 for the xattr-walk result.
	size, err := cli.Raw().Txattrwalk(ctx, 2, 3, "user.existing")
	if err != nil {
		t.Fatalf("Txattrwalk: %v", err)
	}
	if size != uint64(len("old-value")) {
		t.Errorf("Txattrwalk size = %d, want %d", size, len("old-value"))
	}
	// Clean up the xattr-read fid.
	if err := cli.Raw().Clunk(ctx, 3); err != nil {
		t.Fatalf("Clunk xattr-read fid: %v", err)
	}

	// Txattrcreate for "user.new", then Write + Clunk to commit.
	// Clone fresh fid=4 from the x node so Txattrcreate can mutate it.
	walkFresh(t, cli, 2, 4)
	value := []byte("hello")
	if err := cli.Raw().Txattrcreate(ctx, 4, "user.new", uint64(len(value)), 0); err != nil {
		t.Fatalf("Txattrcreate: %v", err)
	}
	if n, err := cli.Raw().Write(ctx, 4, 0, value); err != nil || n != uint32(len(value)) {
		t.Fatalf("Write xattr: n=%d err=%v", n, err)
	}
	if err := cli.Raw().Clunk(ctx, 4); err != nil {
		t.Fatalf("Clunk xattr-write fid: %v", err)
	}
	// Verify the server saw the set.
	if xfile.lastSet != "user.new" {
		t.Errorf("server lastSet = %q, want user.new", xfile.lastSet)
	}
	if got, ok := xfile.attrs["user.new"]; !ok || string(got) != "hello" {
		t.Errorf("server attrs[user.new] = %q (ok=%v), want \"hello\"", got, ok)
	}
}

func TestRaw_Tlock_Tgetlock_RoundTrip(t *testing.T) {
	t.Parallel()
	gen := &server.QIDGenerator{}
	root := memfs.NewDir(gen)

	lk := &rawTestLockerNode{}
	lk.Init(gen.Next(proto.QTFILE), lk)
	root.AddChild("lk", lk.EmbeddedInode())

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	attachRoot(t, cli, 1)
	walkTo(t, cli, 1, 2, []string{"lk"})
	// Lock requires an opened fid per 9P Tlock semantics (server-side
	// state machine requires fidOpen).
	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	if _, _, err := cli.Lopen(ctx, 2, 0 /*O_RDONLY*/); err != nil {
		t.Fatalf("Lopen: %v", err)
	}

	status, err := cli.Raw().Tlock(ctx, 2, proto.LockTypeWrLck, 0, 0, 1024, 42, "me")
	if err != nil {
		t.Fatalf("Tlock: %v", err)
	}
	if status != proto.LockStatusOK {
		t.Errorf("Tlock status = %d, want OK", status)
	}

	gl, err := cli.Raw().Tgetlock(ctx, 2, proto.LockTypeWrLck, 0, 1024, 42, "me")
	if err != nil {
		t.Fatalf("Tgetlock: %v", err)
	}
	if gl.LockType != proto.LockTypeUnlck {
		t.Errorf("Tgetlock LockType = %d, want Unlck", gl.LockType)
	}
}

func TestRaw_Trename_RoundTrip(t *testing.T) {
	t.Parallel()
	root := newRawRUDir(t)
	// Pre-seed a file "a" under root so we can rename it in place.
	gen := root.gen
	file := &memfs.MemFile{}
	file.Init(gen.Next(proto.QTFILE), file)
	root.AddChild("a", file.EmbeddedInode())

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	attachRoot(t, cli, 1)
	walkTo(t, cli, 1, 2, []string{"a"})

	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	// Rename fid=2 (which points at "a") into dfid=1 (root) under "b".
	if err := cli.Raw().Trename(ctx, 2, 1, "b"); err != nil {
		t.Fatalf("Trename: %v", err)
	}
	if _, ok := root.Children()["b"]; !ok {
		t.Error("rename target \"b\" not present in root")
	}
	if _, ok := root.Children()["a"]; ok {
		t.Error("rename source \"a\" still present in root")
	}
}

func TestRaw_Trenameat_RoundTrip(t *testing.T) {
	t.Parallel()
	root := newRawRUDir(t)
	gen := root.gen
	// Source file under root.
	file := &memfs.MemFile{}
	file.Init(gen.Next(proto.QTFILE), file)
	root.AddChild("a", file.EmbeddedInode())

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	attachRoot(t, cli, 1)

	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	// Renameat within root: (root, "a") -> (root, "b").
	if err := cli.Raw().Trenameat(ctx, 1, "a", 1, "b"); err != nil {
		t.Fatalf("Trenameat: %v", err)
	}
	if _, ok := root.Children()["b"]; !ok {
		t.Error("renameat target \"b\" not present")
	}
}

func TestRaw_Tunlinkat_RoundTrip(t *testing.T) {
	t.Parallel()
	root := newRawRUDir(t)
	gen := root.gen
	file := &memfs.MemFile{}
	file.Init(gen.Next(proto.QTFILE), file)
	root.AddChild("a", file.EmbeddedInode())

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	attachRoot(t, cli, 1)
	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	if err := cli.Raw().Tunlinkat(ctx, 1, "a", 0); err != nil {
		t.Fatalf("Tunlinkat: %v", err)
	}
	if _, ok := root.Children()["a"]; ok {
		t.Error("file \"a\" still present after Tunlinkat")
	}
}

func TestRaw_Tlink_RoundTrip(t *testing.T) {
	t.Parallel()
	root := newRawRUDir(t)
	gen := root.gen
	file := &memfs.MemFile{}
	file.Init(gen.Next(proto.QTFILE), file)
	root.AddChild("orig", file.EmbeddedInode())

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	attachRoot(t, cli, 1)
	walkTo(t, cli, 1, 2, []string{"orig"})

	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	// Tlink: dfid=1 (root), fid=2 (orig), name="alias".
	if err := cli.Raw().Tlink(ctx, 1, 2, "alias"); err != nil {
		t.Fatalf("Tlink: %v", err)
	}
	if _, ok := root.Children()["alias"]; !ok {
		t.Error("hard-link \"alias\" not present after Tlink")
	}
}

func TestRaw_Tmknod_RoundTrip(t *testing.T) {
	t.Parallel()
	root := newRawRUDir(t)
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	attachRoot(t, cli, 1)
	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	qid, err := cli.Raw().Tmknod(ctx, 1, "dev0", uint32(proto.FileMode(0o600)), 10, 42, 0)
	if err != nil {
		t.Fatalf("Tmknod: %v", err)
	}
	// QID.Type for a device is QTFILE (0x00) — sanity: not a symlink/dir.
	if qid.Type&proto.QTDIR != 0 {
		t.Errorf("Tmknod QID.Type = %#x, unexpected dir bit", qid.Type)
	}
	if _, ok := root.Children()["dev0"]; !ok {
		t.Error("device node \"dev0\" not present after Tmknod")
	}
}

// TestRaw_Tremove_RoundTrip exercises the wire-level Raw.Tremove path.
//
// ninep's server does not implement a Tremove handler (Phase 21 clients
// prefer Tunlinkat; the server falls through to ENOSYS). The test
// verifies the round-trip machinery works: write Tremove, read Rlerror,
// surface a *client.Error — the full toError path. Once a server-side
// Tremove handler lands, this test can be re-pointed at it to assert
// success + fid invalidation; the Raw wire primitive is unchanged either
// way.
func TestRaw_Tremove_RoundTrip(t *testing.T) {
	t.Parallel()
	root := newRawRUDir(t)
	gen := root.gen
	file := &memfs.MemFile{}
	file.Init(gen.Next(proto.QTFILE), file)
	root.AddChild("gone", file.EmbeddedInode())

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	attachRoot(t, cli, 1)
	walkTo(t, cli, 1, 2, []string{"gone"})

	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	err := cli.Raw().Tremove(ctx, 2)
	if err == nil {
		// Server grew Tremove support — fall through to the
		// fid-invalidation assertion (9P spec: Tremove clunks fid
		// regardless of success).
		if _, ok := root.Children()["gone"]; ok {
			t.Error("file \"gone\" still present after Tremove")
		}
		if err := cli.Raw().Clunk(ctx, 2); err == nil {
			t.Fatal("post-Tremove Clunk(fid=2): want error, got nil")
		}
		return
	}

	// Current ninep server: Tremove surfaces as *client.Error(ENOSYS).
	var cerr *client.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("Tremove err = %v (%T), want *client.Error", err, err)
	}
	if cerr.Errno != proto.ENOSYS {
		t.Logf("Tremove Errno = %v (accepting any server-reported errno)", cerr.Errno)
	}
}

// -- dialect gate tests (external package — can't reach internal
// protocolL/U constants, so we dial a .u mock server and invoke each
// .L-only method; Conn.Dialect() confirms the dialect)---

func TestRaw_Tstatfs_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()
	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	if _, err := cli.Raw().Tstatfs(ctx, 0); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Tstatfs err = %v, want ErrNotSupported", err)
	}
}

func TestRaw_Tgetattr_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()
	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	if _, err := cli.Raw().Tgetattr(ctx, 0, proto.AttrBasic); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Tgetattr err = %v, want ErrNotSupported", err)
	}
}

func TestRaw_Tsetattr_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()
	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	if err := cli.Raw().Tsetattr(ctx, 0, proto.SetAttr{}); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Tsetattr err = %v, want ErrNotSupported", err)
	}
}

func TestRaw_Tsymlink_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()
	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	if _, err := cli.Raw().Tsymlink(ctx, 0, "l", "t", 0); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Tsymlink err = %v, want ErrNotSupported", err)
	}
}

func TestRaw_Treadlink_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()
	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	if _, err := cli.Raw().Treadlink(ctx, 0); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Treadlink err = %v, want ErrNotSupported", err)
	}
}

func TestRaw_Tlock_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()
	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	if _, err := cli.Raw().Tlock(ctx, 0, proto.LockTypeRdLck, 0, 0, 0, 0, ""); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Tlock err = %v, want ErrNotSupported", err)
	}
}

func TestRaw_Tgetlock_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()
	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	if _, err := cli.Raw().Tgetlock(ctx, 0, proto.LockTypeRdLck, 0, 0, 0, ""); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Tgetlock err = %v, want ErrNotSupported", err)
	}
}

func TestRaw_Txattrwalk_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()
	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	if _, err := cli.Raw().Txattrwalk(ctx, 0, 1, "name"); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Txattrwalk err = %v, want ErrNotSupported", err)
	}
}

func TestRaw_Txattrcreate_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()
	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	if err := cli.Raw().Txattrcreate(ctx, 0, "name", 0, 0); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Txattrcreate err = %v, want ErrNotSupported", err)
	}
}

func TestRaw_Tlink_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()
	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	if err := cli.Raw().Tlink(ctx, 0, 1, "x"); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Tlink err = %v, want ErrNotSupported", err)
	}
}

func TestRaw_Tmknod_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()
	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	if _, err := cli.Raw().Tmknod(ctx, 0, "x", 0, 0, 0, 0); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Tmknod err = %v, want ErrNotSupported", err)
	}
}

func TestRaw_Trename_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()
	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	if err := cli.Raw().Trename(ctx, 0, 1, "x"); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Trename err = %v, want ErrNotSupported", err)
	}
}

func TestRaw_Trenameat_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()
	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	if err := cli.Raw().Trenameat(ctx, 0, "a", 1, "b"); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Trenameat err = %v, want ErrNotSupported", err)
	}
}

func TestRaw_Tunlinkat_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()
	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	if err := cli.Raw().Tunlinkat(ctx, 0, "x", 0); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Tunlinkat err = %v, want ErrNotSupported", err)
	}
}

// Tstat is .u-only; on a .L-negotiated Conn, expect ErrNotSupported.
func TestRaw_Tstat_NotSupportedOnL(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	// Default buildTestRoot pair negotiates .L — no Attach needed for
	// the gate test.
	ctx, cancel := rawAdvCtx(t)
	defer cancel()
	if _, err := cli.Raw().Tstat(ctx, 0); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Tstat err = %v, want ErrNotSupported", err)
	}
}

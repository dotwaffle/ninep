package fstest

import (
	"bytes"
	"fmt"
	"syscall"
	"testing"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
	"github.com/dotwaffle/ninep/server"
)

func init() {
	Cases = []TestCase{
		// Walk cases
		{Name: "walk/root", Run: testWalkRoot},
		{Name: "walk/child", Run: testWalkChild},
		{Name: "walk/deep", Run: testWalkDeep},
		{Name: "walk/nonexistent", Run: testWalkNonexistent},
		{Name: "walk/clone", Run: testWalkClone},

		// Read/Write cases
		{Name: "read/basic", Run: testReadBasic},
		{Name: "read/offset", Run: testReadOffset},
		{Name: "read/past-eof", Run: testReadPastEOF},
		{Name: "write/basic", Run: testWriteBasic},
		{Name: "write/grow", Run: testWriteGrow},
		{Name: "write/sparse", Run: testWriteSparse},

		// Setattr cases
		{Name: "setattr/truncate", Run: testSetattrTruncate},
		{Name: "setattr/extend", Run: testSetattrExtend},
		{Name: "setattr/after-parent-clunk", Run: testSetattrAfterParentClunk},

		// Directory cases
		{Name: "readdir/basic", Run: testReaddirBasic},
		{Name: "readdir/empty", Run: testReaddirEmpty},
		{Name: "readdir/paginated", Run: testReaddirPaginated},

		// Rename cases
		{Name: "rename/file", Run: testRenameFile},

		// Create/Mkdir cases
		{Name: "create/file", Run: testCreateFile},
		{Name: "mkdir", Run: testMkdir},

		// Attribute cases
		{Name: "getattr/file", Run: testGetattrFile},
		{Name: "getattr/dir", Run: testGetattrDir},

		// Error cases
		{Name: "error/walk-from-file", Run: testErrorWalkFromFile},
		{Name: "error/read-dir", Run: testErrorReadDir},

		// Symlink/link cases
		{Name: "symlink/roundtrip", Run: testSymlinkRoundtrip},
		{Name: "link/hardlink", Run: testHardlink},

		// Filesystem-level cases
		{Name: "statfs", Run: testStatfs},
		{Name: "fsync", Run: testFsync},

		// Unlink cases
		{Name: "unlink/file", Run: testUnlinkFile},

		// Concurrency cases
		{Name: "concurrent/read", Run: testConcurrentRead},
	}
}

// --- Walk test cases ---

func testWalkRoot(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	ra := attach(t, tc, 1, 0, "test", "")
	if ra.QID.Type != proto.QTDIR {
		t.Errorf("root QID type = %d, want QTDIR (%d)", ra.QID.Type, proto.QTDIR)
	}
}

func testWalkChild(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	msg := walk(t, tc, 2, 0, 1, "file.txt")
	rw := expectRwalk(t, msg)
	if len(rw.QIDs) != 1 {
		t.Fatalf("walk QIDs count = %d, want 1", len(rw.QIDs))
	}
	if rw.QIDs[0].Type != proto.QTFILE {
		t.Errorf("file.txt QID type = %d, want QTFILE (%d)", rw.QIDs[0].Type, proto.QTFILE)
	}
}

func testWalkDeep(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	msg := walk(t, tc, 2, 0, 1, "sub", "nested.txt")
	rw := expectRwalk(t, msg)
	if len(rw.QIDs) != 2 {
		t.Fatalf("walk QIDs count = %d, want 2", len(rw.QIDs))
	}
	if rw.QIDs[0].Type != proto.QTDIR {
		t.Errorf("sub QID type = %d, want QTDIR", rw.QIDs[0].Type)
	}
	if rw.QIDs[1].Type != proto.QTFILE {
		t.Errorf("nested.txt QID type = %d, want QTFILE", rw.QIDs[1].Type)
	}
}

func testWalkNonexistent(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	msg := walk(t, tc, 2, 0, 1, "doesnotexist")
	expectRlerror(t, msg, proto.ENOENT)
}

func testWalkClone(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	// Clone: walk with empty names, different fid.
	msg := walk(t, tc, 2, 0, 1)
	rw := expectRwalk(t, msg)
	if len(rw.QIDs) != 0 {
		t.Errorf("clone walk QIDs = %d, want 0", len(rw.QIDs))
	}

	// Verify cloned fid works by clunking it.
	clunkMsg := clunk(t, tc, 3, 1)
	if _, ok := clunkMsg.(*proto.Rclunk); !ok {
		t.Fatalf("expected Rclunk for cloned fid, got %T", clunkMsg)
	}
}

// --- Read/Write test cases ---

func testReadBasic(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	// Walk to file.txt.
	msg := walk(t, tc, 2, 0, 1, "file.txt")
	expectRwalk(t, msg)

	// Open.
	msg = open(t, tc, 3, 1, syscall.O_RDONLY)
	if _, ok := msg.(*p9l.Rlopen); !ok {
		t.Fatalf("expected Rlopen, got %T: %+v", msg, msg)
	}

	// Read.
	msg = read(t, tc, 4, 1, 0, 4096)
	data := expectRread(t, msg)
	if !bytes.Equal(data, []byte("hello world")) {
		t.Errorf("read data = %q, want %q", data, "hello world")
	}
}

func testReadOffset(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	msg := walk(t, tc, 2, 0, 1, "file.txt")
	expectRwalk(t, msg)

	msg = open(t, tc, 3, 1, syscall.O_RDONLY)
	if _, ok := msg.(*p9l.Rlopen); !ok {
		t.Fatalf("expected Rlopen, got %T: %+v", msg, msg)
	}

	// Read from offset 6.
	msg = read(t, tc, 4, 1, 6, 4096)
	data := expectRread(t, msg)
	if !bytes.Equal(data, []byte("world")) {
		t.Errorf("read at offset 6 = %q, want %q", data, "world")
	}
}

func testReadPastEOF(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	msg := walk(t, tc, 2, 0, 1, "file.txt")
	expectRwalk(t, msg)

	msg = open(t, tc, 3, 1, syscall.O_RDONLY)
	if _, ok := msg.(*p9l.Rlopen); !ok {
		t.Fatalf("expected Rlopen, got %T: %+v", msg, msg)
	}

	// Read past end of file.
	msg = read(t, tc, 4, 1, 1000, 4096)
	data := expectRread(t, msg)
	if len(data) != 0 {
		t.Errorf("read past EOF returned %d bytes, want 0", len(data))
	}
}

func testWriteBasic(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	msg := walk(t, tc, 2, 0, 1, "file.txt")
	expectRwalk(t, msg)

	msg = open(t, tc, 3, 1, syscall.O_RDWR)
	if _, ok := msg.(*p9l.Rlopen); !ok {
		t.Fatalf("expected Rlopen, got %T: %+v", msg, msg)
	}

	// Write new data.
	writeData := []byte("replaced content")
	msg = write(t, tc, 4, 1, 0, writeData)
	count := expectRwrite(t, msg)
	if count != uint32(len(writeData)) {
		t.Errorf("write count = %d, want %d", count, len(writeData))
	}

	// Read back to verify.
	msg = read(t, tc, 5, 1, 0, 4096)
	data := expectRread(t, msg)
	if !bytes.Equal(data[:len(writeData)], writeData) {
		t.Errorf("read after write = %q, want prefix %q", data, writeData)
	}
}

// createScratch clones the root fid, creates a fresh read-write file
// named on it, and returns with fid 1 left as the open created handle.
// New write/setattr cases use a scratch file rather than file.txt so
// they do not depend on mutations write/basic makes to a shared root.
func createScratch(t *testing.T, tc *testConn, name string) {
	t.Helper()
	attach(t, tc, 1, 0, "test", "")
	expectRwalk(t, walk(t, tc, 2, 0, 1))
	msg := create(t, tc, 3, 1, name, syscall.O_RDWR, 0o644, 0)
	if _, ok := msg.(*p9l.Rlcreate); !ok {
		t.Fatalf("expected Rlcreate for %q, got %T: %+v", name, msg, msg)
	}
}

func testWriteGrow(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	createScratch(t, tc, "growfile")

	// Write at offset 0, then append a second chunk past the current end
	// of file. The reported size must cover both writes and a full read
	// must return their concatenation.
	first := []byte("hello")
	if n := expectRwrite(t, write(t, tc, 4, 1, 0, first)); n != uint32(len(first)) {
		t.Fatalf("write first: count = %d, want %d", n, len(first))
	}
	second := []byte("world")
	if n := expectRwrite(t, write(t, tc, 5, 1, uint64(len(first)), second)); n != uint32(len(second)) {
		t.Fatalf("write second: count = %d, want %d", n, len(second))
	}

	want := append(append([]byte{}, first...), second...)
	rga := expectRgetattr(t, getattr(t, tc, 6, 1, proto.AttrAll))
	if rga.Attr.Size != uint64(len(want)) {
		t.Errorf("grown size = %d, want %d", rga.Attr.Size, len(want))
	}
	data := expectRread(t, read(t, tc, 7, 1, 0, 4096))
	if !bytes.Equal(data, want) {
		t.Errorf("read after grow = %q, want %q", data, want)
	}
}

func testWriteSparse(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	createScratch(t, tc, "sparsefile")

	// Write at a non-zero offset on an empty file. The gap before the
	// payload must read back as zero fill and the size must cover it.
	const gap = 8
	payload := []byte("tail")
	if n := expectRwrite(t, write(t, tc, 4, 1, gap, payload)); n != uint32(len(payload)) {
		t.Fatalf("sparse write: count = %d, want %d", n, len(payload))
	}

	wantSize := uint64(gap) + uint64(len(payload))
	rga := expectRgetattr(t, getattr(t, tc, 5, 1, proto.AttrAll))
	if rga.Attr.Size != wantSize {
		t.Errorf("sparse size = %d, want %d", rga.Attr.Size, wantSize)
	}

	data := expectRread(t, read(t, tc, 6, 1, 0, 4096))
	if uint64(len(data)) != wantSize {
		t.Fatalf("sparse read len = %d, want %d", len(data), wantSize)
	}
	for i := range gap {
		if data[i] != 0 {
			t.Errorf("sparse gap byte %d = %d, want 0", i, data[i])
		}
	}
	if !bytes.Equal(data[gap:], payload) {
		t.Errorf("sparse tail = %q, want %q", data[gap:], payload)
	}
}

func testSetattrTruncate(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	createScratch(t, tc, "truncfile")

	content := []byte("0123456789")
	expectRwrite(t, write(t, tc, 4, 1, 0, content))

	// Shrink to 4 bytes via Tsetattr(size).
	if _, ok := setattr(t, tc, 5, 1, proto.SetAttr{Valid: proto.SetAttrSize, Size: 4}).(*p9l.Rsetattr); !ok {
		t.Fatalf("expected Rsetattr for truncate")
	}
	rga := expectRgetattr(t, getattr(t, tc, 6, 1, proto.AttrAll))
	if rga.Attr.Size != 4 {
		t.Errorf("size after truncate = %d, want 4", rga.Attr.Size)
	}
	data := expectRread(t, read(t, tc, 7, 1, 0, 4096))
	if !bytes.Equal(data, content[:4]) {
		t.Errorf("read after truncate = %q, want %q", data, content[:4])
	}
}

// testSetattrAfterParentClunk: a node must stay fully operable after every
// fid referencing its parent directory is clunked. Filesystems that anchor
// child operations to a parent directory handle (passthrough's *at
// syscalls) must give the child its own anchor; borrowing the parent
// node's descriptor fails with EBADF -- or hits a reused descriptor --
// once the parent is released.
func testSetattrAfterParentClunk(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	// fid 1 -> sub, fid 2 -> sub/nested.txt through fid 1.
	expectRwalk(t, walk(t, tc, 2, 0, 1, "sub"))
	expectRwalk(t, walk(t, tc, 3, 1, 2, "nested.txt"))

	// Drop every reference to the parent directory.
	if _, ok := clunk(t, tc, 4, 1).(*proto.Rclunk); !ok {
		t.Fatalf("expected Rclunk for parent fid")
	}

	// Attribute reads and parent-anchored attribute writes (utimes) must
	// still succeed on the child. Whether the mtime value persists is a
	// separate capability (memfs does not store times); the signal here is
	// that neither operation fails with EBADF on a stale parent anchor.
	rga := expectRgetattr(t, getattr(t, tc, 5, 2, proto.AttrAll))
	if _, ok := setattr(t, tc, 6, 2, proto.SetAttr{
		Valid:    proto.SetAttrMTime,
		MTimeSec: rga.Attr.MTimeSec + 1,
	}).(*p9l.Rsetattr); !ok {
		t.Fatalf("expected Rsetattr on child after parent clunk")
	}
	expectRgetattr(t, getattr(t, tc, 7, 2, proto.AttrAll))
}

func testSetattrExtend(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	createScratch(t, tc, "extendfile")

	content := []byte("abc")
	expectRwrite(t, write(t, tc, 4, 1, 0, content))

	// Grow to 8 bytes via Tsetattr(size); the new tail must read as zero.
	if _, ok := setattr(t, tc, 5, 1, proto.SetAttr{Valid: proto.SetAttrSize, Size: 8}).(*p9l.Rsetattr); !ok {
		t.Fatalf("expected Rsetattr for extend")
	}
	rga := expectRgetattr(t, getattr(t, tc, 6, 1, proto.AttrAll))
	if rga.Attr.Size != 8 {
		t.Errorf("size after extend = %d, want 8", rga.Attr.Size)
	}
	data := expectRread(t, read(t, tc, 7, 1, 0, 4096))
	if len(data) != 8 {
		t.Fatalf("read after extend len = %d, want 8", len(data))
	}
	if !bytes.Equal(data[:3], content) {
		t.Errorf("extend head = %q, want %q", data[:3], content)
	}
	for i := 3; i < 8; i++ {
		if data[i] != 0 {
			t.Errorf("extend tail byte %d = %d, want 0", i, data[i])
		}
	}
}

// --- Directory test cases ---

func testReaddirBasic(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	// Open the root directory for readdir.
	msg := open(t, tc, 2, 0, syscall.O_RDONLY)
	if _, ok := msg.(*p9l.Rlopen); !ok {
		t.Fatalf("expected Rlopen, got %T: %+v", msg, msg)
	}

	// Readdir.
	msg = readdir(t, tc, 3, 0, 0, 65536)
	rdr, ok := msg.(*p9l.Rreaddir)
	if !ok {
		t.Fatalf("expected Rreaddir, got %T: %+v", msg, msg)
	}

	dirents := parseDirents(rdr.Data)
	if len(dirents) < 3 {
		t.Fatalf("readdir returned %d entries, want at least 3 (file.txt, empty, sub)", len(dirents))
	}

	// Verify expected entries are present.
	names := make(map[string]bool)
	for _, d := range dirents {
		names[d.Name] = true
	}
	for _, expected := range []string{"file.txt", "empty", "sub"} {
		if !names[expected] {
			t.Errorf("readdir missing entry %q", expected)
		}
	}
}

func testReaddirEmpty(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	// Walk to sub/, create an empty directory, walk into it.
	msg := walk(t, tc, 2, 0, 1, "sub")
	expectRwalk(t, msg)

	// Mkdir "emptydir" in sub.
	msg = mkdir(t, tc, 3, 1, "emptydir", 0o755, 0)
	if _, ok := msg.(*p9l.Rmkdir); !ok {
		t.Fatalf("expected Rmkdir, got %T: %+v", msg, msg)
	}

	// Walk to emptydir.
	msg = walk(t, tc, 4, 1, 2, "emptydir")
	expectRwalk(t, msg)

	// Open emptydir.
	msg = open(t, tc, 5, 2, syscall.O_RDONLY)
	if _, ok := msg.(*p9l.Rlopen); !ok {
		t.Fatalf("expected Rlopen, got %T: %+v", msg, msg)
	}

	// Readdir on empty directory.
	msg = readdir(t, tc, 6, 2, 0, 65536)
	rdr, ok := msg.(*p9l.Rreaddir)
	if !ok {
		t.Fatalf("expected Rreaddir, got %T: %+v", msg, msg)
	}

	dirents := parseDirents(rdr.Data)
	if len(dirents) != 0 {
		t.Errorf("readdir on empty dir returned %d entries, want 0", len(dirents))
	}
}

// testReaddirPaginated verifies that a client resuming Treaddir with the
// previous batch's last-entry Offset as its next request's Offset (and a
// count too small to fit every entry in one round trip) eventually
// enumerates the whole directory exactly once, in whatever number of
// round trips the small count forces.
func testReaddirPaginated(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	msg := mkdir(t, tc, 2, 0, "paginated", 0o755, 0)
	if _, ok := msg.(*p9l.Rmkdir); !ok {
		t.Fatalf("expected Rmkdir, got %T: %+v", msg, msg)
	}
	expectRwalk(t, walk(t, tc, 3, 0, 1, "paginated"))

	const numEntries = 25
	want := make(map[string]bool, numEntries)
	var tag proto.Tag = 4
	for i := range numEntries {
		name := fmt.Sprintf("f%02d", i)
		want[name] = true

		// Clone the directory fid (1) to a fresh fid (2) for Tlcreate,
		// which consumes whatever fid it is given -- fid 1 must survive
		// for the next iteration and the final Readdir.
		expectRwalk(t, walk(t, tc, tag, 1, 2))
		tag++
		msg = create(t, tc, tag, 2, name, syscall.O_RDWR, 0o644, 0)
		tag++
		if _, ok := msg.(*p9l.Rlcreate); !ok {
			t.Fatalf("create %q: expected Rlcreate, got %T: %+v", name, msg, msg)
		}
		clunk(t, tc, tag, 2)
		tag++
	}

	msg = open(t, tc, tag, 1, syscall.O_RDONLY)
	tag++
	if _, ok := msg.(*p9l.Rlopen); !ok {
		t.Fatalf("expected Rlopen, got %T: %+v", msg, msg)
	}

	// count=96 fits only a few of these short-named entries per Rreaddir,
	// forcing several round trips to drain the directory.
	got := make(map[string]bool, numEntries)
	var offset uint64
	for range numEntries + 2 {
		msg = readdir(t, tc, tag, 1, offset, 96)
		tag++
		rdr, ok := msg.(*p9l.Rreaddir)
		if !ok {
			t.Fatalf("expected Rreaddir, got %T: %+v", msg, msg)
		}
		dirents := parseDirents(rdr.Data)
		if len(dirents) == 0 {
			break
		}
		for _, d := range dirents {
			if got[d.Name] {
				t.Errorf("paginated readdir returned duplicate entry %q", d.Name)
			}
			got[d.Name] = true
			offset = d.Offset
		}
	}

	if len(got) != len(want) {
		t.Fatalf("paginated readdir returned %d entries, want %d (got=%v)", len(got), len(want), got)
	}
	for name := range want {
		if !got[name] {
			t.Errorf("paginated readdir missing entry %q", name)
		}
	}
}

// --- Rename test cases ---

// testRenameFile exercises Trenameat on a scratch file created for this
// case, moving it to a new name in the same directory. NodeRenamer is an
// optional capability (memfs.MemDir does not implement it): a root that
// returns ENOSYS is treated as "capability not implemented" rather than
// a failure, matching this project's opt-in capability pattern.
func testRenameFile(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	// Clone root fid for create, then create the scratch file to rename.
	expectRwalk(t, walk(t, tc, 2, 0, 1))
	msg := create(t, tc, 3, 1, "rename-src", syscall.O_RDWR, 0o644, 0)
	if _, ok := msg.(*p9l.Rlcreate); !ok {
		t.Fatalf("expected Rlcreate, got %T: %+v", msg, msg)
	}
	clunk(t, tc, 4, 1)

	msg = renameat(t, tc, 5, 0, "rename-src", 0, "rename-dst")
	if rlerr, ok := msg.(*p9l.Rlerror); ok {
		if rlerr.Ecode == proto.ENOSYS {
			t.Skip("root does not implement NodeRenamer")
		}
		t.Fatalf("Trenameat: unexpected error %v", rlerr.Ecode)
	}
	if _, ok := msg.(*p9l.Rrenameat); !ok {
		t.Fatalf("expected Rrenameat, got %T: %+v", msg, msg)
	}

	// Old name is gone.
	expectRlerror(t, walk(t, tc, 6, 0, 2, "rename-src"), proto.ENOENT)

	// New name resolves to the same file.
	rw := expectRwalk(t, walk(t, tc, 7, 0, 3, "rename-dst"))
	if len(rw.QIDs) != 1 {
		t.Fatalf("walk to rename-dst: QIDs = %d, want 1", len(rw.QIDs))
	}
}

// --- Create/Mkdir test cases ---

func testCreateFile(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	// Clone root fid for create (create consumes the fid).
	msg := walk(t, tc, 2, 0, 1)
	expectRwalk(t, msg)

	// Create "newfile" in root. Tlcreate replaces fid 1 with the new file.
	msg = create(t, tc, 3, 1, "newfile", syscall.O_RDWR, 0o644, 0)
	if _, ok := msg.(*p9l.Rlcreate); !ok {
		t.Fatalf("expected Rlcreate, got %T: %+v", msg, msg)
	}

	// Write to the created file (fid 1 is now open on "newfile").
	writeData := []byte("new content")
	msg = write(t, tc, 4, 1, 0, writeData)
	expectRwrite(t, msg)

	// Clunk fid 1.
	clunk(t, tc, 5, 1)

	// Walk to "newfile" to verify it exists.
	msg = walk(t, tc, 6, 0, 2, "newfile")
	rw := expectRwalk(t, msg)
	if len(rw.QIDs) != 1 {
		t.Fatalf("walk to newfile: QIDs = %d, want 1", len(rw.QIDs))
	}

	// Open and read back.
	msg = open(t, tc, 7, 2, syscall.O_RDONLY)
	if _, ok := msg.(*p9l.Rlopen); !ok {
		t.Fatalf("expected Rlopen, got %T: %+v", msg, msg)
	}

	msg = read(t, tc, 8, 2, 0, 4096)
	data := expectRread(t, msg)
	if !bytes.Equal(data, writeData) {
		t.Errorf("read created file = %q, want %q", data, writeData)
	}
}

func testMkdir(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	// Mkdir "newdir" in root.
	msg := mkdir(t, tc, 2, 0, "newdir", 0o755, 0)
	rmkdir, ok := msg.(*p9l.Rmkdir)
	if !ok {
		t.Fatalf("expected Rmkdir, got %T: %+v", msg, msg)
	}
	if rmkdir.QID.Type != proto.QTDIR {
		t.Errorf("mkdir QID type = %d, want QTDIR (%d)", rmkdir.QID.Type, proto.QTDIR)
	}

	// Walk into newdir to verify.
	msg = walk(t, tc, 3, 0, 1, "newdir")
	rw := expectRwalk(t, msg)
	if len(rw.QIDs) != 1 {
		t.Fatalf("walk to newdir: QIDs = %d, want 1", len(rw.QIDs))
	}
	if rw.QIDs[0].Type != proto.QTDIR {
		t.Errorf("newdir QID type = %d, want QTDIR", rw.QIDs[0].Type)
	}
}

// --- Attribute test cases ---

func testGetattrFile(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	// Use sub/nested.txt instead of file.txt because write/basic may
	// have modified file.txt on a shared root.
	msg := walk(t, tc, 2, 0, 1, "sub", "nested.txt")
	rw := expectRwalk(t, msg)
	if len(rw.QIDs) != 2 {
		t.Fatalf("walk QIDs count = %d, want 2", len(rw.QIDs))
	}

	msg = getattr(t, tc, 3, 1, proto.AttrAll)
	rga, ok := msg.(*p9l.Rgetattr)
	if !ok {
		t.Fatalf("expected Rgetattr, got %T: %+v", msg, msg)
	}

	if rga.Attr.Size != uint64(len("nested content")) {
		t.Errorf("file size = %d, want %d", rga.Attr.Size, len("nested content"))
	}
	// Mode should not have directory bit set.
	if rga.Attr.Mode&0o040000 != 0 {
		t.Errorf("file mode has directory bit set: %#o", rga.Attr.Mode)
	}
}

func testGetattrDir(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	msg := getattr(t, tc, 2, 0, proto.AttrAll)
	rga, ok := msg.(*p9l.Rgetattr)
	if !ok {
		t.Fatalf("expected Rgetattr, got %T: %+v", msg, msg)
	}

	// Mode should have directory bit set.
	if rga.Attr.Mode&0o040000 == 0 {
		t.Errorf("dir mode missing directory bit: %#o", rga.Attr.Mode)
	}
}

// --- Error test cases ---

func testErrorWalkFromFile(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	// Walk to file.txt.
	msg := walk(t, tc, 2, 0, 1, "file.txt")
	expectRwalk(t, msg)

	// Try to walk into file.txt (not a directory).
	msg = walk(t, tc, 3, 1, 2, "child")
	expectRlerror(t, msg, proto.ENOTDIR)
}

func testErrorReadDir(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	// Open root directory.
	msg := open(t, tc, 2, 0, syscall.O_RDONLY)
	if _, ok := msg.(*p9l.Rlopen); !ok {
		t.Fatalf("expected Rlopen, got %T: %+v", msg, msg)
	}

	// Try Tread on directory fid -- should return error or empty.
	msg = read(t, tc, 3, 0, 0, 4096)
	// Both Rlerror and Rread with empty data are acceptable for reading
	// from a directory.
	switch resp := msg.(type) {
	case *p9l.Rlerror:
		// Error is the expected behavior for reading a directory.
	case *proto.Rread:
		// Empty read is also acceptable.
		if len(resp.Data) > 0 {
			t.Logf("read on directory returned %d bytes (implementation-defined)", len(resp.Data))
		}
	default:
		t.Fatalf("expected Rlerror or Rread, got %T: %+v", msg, msg)
	}
}

// --- Unlink test cases ---

func testUnlinkFile(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	// Clone root fid for create.
	msg := walk(t, tc, 2, 0, 1)
	expectRwalk(t, msg)

	// Create a file to unlink.
	msg = create(t, tc, 3, 1, "todelete", syscall.O_RDWR, 0o644, 0)
	if _, ok := msg.(*p9l.Rlcreate); !ok {
		t.Fatalf("expected Rlcreate, got %T: %+v", msg, msg)
	}
	clunk(t, tc, 4, 1)

	// Unlink "todelete" from root (fid 0).
	msg = unlink(t, tc, 5, 0, "todelete", 0)
	if _, ok := msg.(*p9l.Runlinkat); !ok {
		t.Fatalf("expected Runlinkat, got %T: %+v", msg, msg)
	}

	// Verify it's gone.
	msg = walk(t, tc, 6, 0, 2, "todelete")
	expectRlerror(t, msg, proto.ENOENT)
}

// --- Concurrency test cases ---

func testConcurrentRead(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	// Use sub/nested.txt to avoid interference from write/basic on
	// shared roots.
	msg := walk(t, tc, 2, 0, 1, "sub", "nested.txt")
	rw := expectRwalk(t, msg)
	if len(rw.QIDs) != 2 {
		t.Fatalf("walk QIDs count = %d, want 2", len(rw.QIDs))
	}

	msg = open(t, tc, 3, 1, syscall.O_RDONLY)
	if _, ok := msg.(*p9l.Rlopen); !ok {
		t.Fatalf("expected Rlopen, got %T: %+v", msg, msg)
	}

	// Send multiple reads sequentially and verify each response.
	// net.Pipe does not support concurrent writes, so we serialize
	// request/response pairs. The server processes these with its
	// goroutine-per-request model, exercising concurrent handler
	// execution on the server side.
	const numReads = 10
	expected := []byte("nested content")

	for i := range numReads {
		tag := proto.Tag(10 + i)
		msg = read(t, tc, tag, 1, 0, 4096)
		data := expectRread(t, msg)
		if !bytes.Equal(data, expected) {
			t.Errorf("read %d: data = %q, want %q", i, data, expected)
		}
	}
}

// testSymlinkRoundtrip: Tsymlink creates a link, walking to it yields a
// QTSYMLINK qid, and Treadlink returns the original target verbatim.
func testSymlinkRoundtrip(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	const target = "sub/nested.txt"
	msg := skipENOSYS(t, symlink(t, tc, 2, 0, "sl", target), "NodeSymlinker")
	rs, ok := msg.(*p9l.Rsymlink)
	if !ok {
		t.Fatalf("expected Rsymlink, got %T: %+v", msg, msg)
	}
	if rs.QID.Type != proto.QTSYMLINK {
		t.Errorf("Rsymlink qid type = %v, want QTSYMLINK", rs.QID.Type)
	}

	rw := expectRwalk(t, walk(t, tc, 3, 0, 1, "sl"))
	if len(rw.QIDs) != 1 || rw.QIDs[0].Type != proto.QTSYMLINK {
		t.Fatalf("walk to symlink: qids = %+v, want one QTSYMLINK", rw.QIDs)
	}

	msg = readlink(t, tc, 4, 1)
	rl, ok := msg.(*p9l.Rreadlink)
	if !ok {
		t.Fatalf("expected Rreadlink, got %T: %+v", msg, msg)
	}
	if rl.Target != target {
		t.Errorf("readlink target = %q, want %q", rl.Target, target)
	}
}

// testHardlink: Tlink adds a second name for a file; both names resolve to
// the same qid path and content, and getattr reports nlink 2 (when the
// filesystem tracks link counts).
func testHardlink(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	createScratch(t, tc, "linksrc")
	expectRwrite(t, write(t, tc, 4, 1, 0, []byte("payload")))
	clunk(t, tc, 5, 1)

	// Re-walk to the file for a clean (unopened) fid to link from.
	expectRwalk(t, walk(t, tc, 6, 0, 1, "linksrc"))
	msg := skipENOSYS(t, link(t, tc, 7, 0, 1, "linkdst"), "NodeLinker")
	if _, ok := msg.(*p9l.Rlink); !ok {
		t.Fatalf("expected Rlink, got %T: %+v", msg, msg)
	}

	rwSrc := expectRwalk(t, walk(t, tc, 8, 0, 2, "linksrc"))
	rwDst := expectRwalk(t, walk(t, tc, 9, 0, 3, "linkdst"))
	if rwSrc.QIDs[0].Path != rwDst.QIDs[0].Path {
		t.Errorf("hardlink qid path = %d, want %d (same inode)", rwDst.QIDs[0].Path, rwSrc.QIDs[0].Path)
	}

	if _, ok := lopen(t, tc, 10, 3, 0).(*p9l.Rlopen); !ok {
		t.Fatalf("open linkdst failed")
	}
	if data := expectRread(t, read(t, tc, 11, 3, 0, 4096)); string(data) != "payload" {
		t.Errorf("read via hardlink = %q, want %q", data, "payload")
	}
}

// testStatfs: Tstatfs on the root fid returns filesystem statistics with a
// sane block size.
func testStatfs(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	msg := skipENOSYS(t, statfs(t, tc, 2, 0), "NodeStatFSer")
	rs, ok := msg.(*p9l.Rstatfs)
	if !ok {
		t.Fatalf("expected Rstatfs, got %T: %+v", msg, msg)
	}
	if rs.Stat.BSize == 0 {
		t.Errorf("statfs bsize = 0, want non-zero")
	}
}

// testFsync: Tfsync on an opened, written fid succeeds.
func testFsync(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	createScratch(t, tc, "fsyncfile")
	expectRwrite(t, write(t, tc, 4, 1, 0, []byte("durable")))

	msg := skipENOSYS(t, fsync(t, tc, 5, 1), "FileSyncer/NodeFsyncer")
	if _, ok := msg.(*p9l.Rfsync); !ok {
		t.Fatalf("expected Rfsync, got %T: %+v", msg, msg)
	}
}

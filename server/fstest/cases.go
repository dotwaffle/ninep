package fstest

import (
	"bytes"
	"sync"
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

		// Directory cases
		{Name: "readdir/basic", Run: testReaddirBasic},
		{Name: "readdir/empty", Run: testReaddirEmpty},

		// Create/Mkdir cases
		{Name: "create/file", Run: testCreateFile},
		{Name: "mkdir", Run: testMkdir},

		// Attribute cases
		{Name: "getattr/file", Run: testGetattrFile},
		{Name: "getattr/dir", Run: testGetattrDir},

		// Error cases
		{Name: "error/walk-from-file", Run: testErrorWalkFromFile},
		{Name: "error/read-dir", Run: testErrorReadDir},

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

	msg := walk(t, tc, 2, 0, 1, "file.txt")
	expectRwalk(t, msg)

	msg = getattr(t, tc, 3, 1, proto.AttrAll)
	rga, ok := msg.(*p9l.Rgetattr)
	if !ok {
		t.Fatalf("expected Rgetattr, got %T: %+v", msg, msg)
	}

	if rga.Attr.Size != uint64(len("hello world")) {
		t.Errorf("file size = %d, want %d", rga.Attr.Size, len("hello world"))
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

	msg := walk(t, tc, 2, 0, 1, "file.txt")
	expectRwalk(t, msg)

	msg = open(t, tc, 3, 1, syscall.O_RDONLY)
	if _, ok := msg.(*p9l.Rlopen); !ok {
		t.Fatalf("expected Rlopen, got %T: %+v", msg, msg)
	}

	// Launch concurrent reads. Note: 9P is a serialized protocol over a
	// single connection, so "concurrent" here means interleaved requests
	// rather than truly parallel I/O. We verify correctness under rapid
	// sequential access.
	const numReads = 10
	var wg sync.WaitGroup
	errCh := make(chan error, numReads)

	for i := range numReads {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			tag := proto.Tag(10 + idx)
			sendMsg(t, tc.client, tag, &proto.Tread{
				Fid:    1,
				Offset: 0,
				Count:  4096,
			})
		}(i)
	}

	wg.Wait()

	// Read all responses.
	for range numReads {
		_, msg := readMsg(t, tc.client)
		rr, ok := msg.(*proto.Rread)
		if !ok {
			errCh <- nil
			continue
		}
		if !bytes.Equal(rr.Data, []byte("hello world")) {
			t.Errorf("concurrent read data = %q, want %q", rr.Data, "hello world")
		}
	}
}

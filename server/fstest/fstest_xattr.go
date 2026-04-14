package fstest

import (
	"bytes"
	"testing"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
	"github.com/dotwaffle/ninep/server"
)

// XattrExpectedTree documents the tree shape a root must provide for
// CheckXattr to pass. The root must contain a file "xfile" that
// implements all four simple xattr capabilities (NodeXattrGetter,
// NodeXattrSetter, NodeXattrLister, NodeXattrRemover). xfile must
// start with the xattr "user.existing" => "existing-value".
//
// Map format: map[filename]map[xattrName]xattrValue.
var XattrExpectedTree = map[string]map[string]string{
	"xfile": {"user.existing": "existing-value"},
}

// XattrCases is the exported slice of xattr test cases used by CheckXattr.
// Ordered non-destructive -> destructive ("xattr/remove" last), though
// CheckXattr's per-case root factory makes order irrelevant for correctness.
//
// Callers MUST NOT mutate XattrCases -- iterate to filter cases.
var XattrCases = []TestCase{
	{Name: "xattr/get", Run: testXattrGet},
	{Name: "xattr/set", Run: testXattrSet},
	{Name: "xattr/list", Run: testXattrList},
	{Name: "xattr/remove", Run: testXattrRemove},
}

// CheckXattr runs every XattrCases entry against a fresh root obtained
// from newRoot. The root must conform to XattrExpectedTree.
//
// CheckXattr is opt-in: callers whose filesystem does not support xattrs
// should NOT call this function. Check() and CheckFactory() do not run
// xattr cases -- filesystems without xattr support are unaffected.
func CheckXattr(t *testing.T, newRoot func(t *testing.T) server.Node) {
	t.Helper()
	for _, tc := range XattrCases {
		t.Run(tc.Name, func(t *testing.T) {
			root := newRoot(t)
			tc.Run(t, root)
		})
	}
}

// testXattrGet: walk to xfile, Txattrwalk("user.existing"), read the
// accumulated xattr data via Tread, expect "existing-value".
func testXattrGet(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	msg := walk(t, tc, 2, 0, 1, "xfile")
	expectRwalk(t, msg)

	msg = xattrwalk(t, tc, 3, 1, 10, "user.existing")
	rxw := expectRxattrwalk(t, msg)
	if rxw.Size != uint64(len("existing-value")) {
		t.Errorf("xattrwalk size = %d, want %d", rxw.Size, len("existing-value"))
	}

	msg = read(t, tc, 4, 10, 0, 1024)
	data := expectRread(t, msg)
	if string(data) != "existing-value" {
		t.Errorf("xattr data = %q, want %q", string(data), "existing-value")
	}

	clunk(t, tc, 5, 10)
}

// testXattrSet: walk to xfile, clone fid, Txattrcreate("user.new",
// Size=5), Twrite "world", Tclunk. Verify by subsequent
// Txattrwalk("user.new") -> size=5, Tread -> "world".
func testXattrSet(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	msg := walk(t, tc, 2, 0, 1, "xfile")
	expectRwalk(t, msg)

	// Clone fid 1 to fid 2 (walk with 0 names); xattrcreate consumes
	// the fid's state, so we operate on the clone.
	msg = walk(t, tc, 3, 1, 2)
	expectRwalk(t, msg)

	msg = xattrcreate(t, tc, 4, 2, "user.new", 5, 0)
	if _, ok := msg.(*p9l.Rxattrcreate); !ok {
		t.Fatalf("expected Rxattrcreate, got %T: %+v", msg, msg)
	}

	msg = write(t, tc, 5, 2, 0, []byte("world"))
	if _, ok := msg.(*proto.Rwrite); !ok {
		t.Fatalf("expected Rwrite, got %T: %+v", msg, msg)
	}

	msg = clunk(t, tc, 6, 2)
	if _, ok := msg.(*proto.Rclunk); !ok {
		t.Fatalf("expected Rclunk, got %T: %+v", msg, msg)
	}

	// Verify by reading back.
	msg = xattrwalk(t, tc, 7, 1, 11, "user.new")
	rxw := expectRxattrwalk(t, msg)
	if rxw.Size != 5 {
		t.Errorf("roundtrip xattrwalk size = %d, want 5", rxw.Size)
	}

	msg = read(t, tc, 8, 11, 0, 1024)
	data := expectRread(t, msg)
	if string(data) != "world" {
		t.Errorf("roundtrip xattr data = %q, want %q", string(data), "world")
	}
	clunk(t, tc, 9, 11)
}

// testXattrList: walk to xfile, Txattrwalk(""), read the list buffer,
// assert it contains "user.existing".
func testXattrList(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	msg := walk(t, tc, 2, 0, 1, "xfile")
	expectRwalk(t, msg)

	msg = xattrwalk(t, tc, 3, 1, 10, "")
	rxw := expectRxattrwalk(t, msg)
	if rxw.Size == 0 {
		t.Fatal("xattr list size should be > 0 when xfile has xattrs")
	}

	msg = read(t, tc, 4, 10, 0, 4096)
	data := expectRread(t, msg)
	if !bytes.Contains(data, []byte("user.existing")) {
		t.Errorf("xattr list = %q, expected to contain %q", string(data), "user.existing")
	}
	clunk(t, tc, 5, 10)
}

// testXattrRemove: walk to xfile, clone, Txattrcreate with Size=0 (no
// intermediate Twrite), Tclunk. Verify removal: subsequent
// Txattrwalk("user.existing") -> Rlerror{ENODATA}.
func testXattrRemove(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	msg := walk(t, tc, 2, 0, 1, "xfile")
	expectRwalk(t, msg)

	msg = walk(t, tc, 3, 1, 2) // clone for xattrcreate
	expectRwalk(t, msg)

	msg = xattrcreate(t, tc, 4, 2, "user.existing", 0, 0)
	if _, ok := msg.(*p9l.Rxattrcreate); !ok {
		t.Fatalf("expected Rxattrcreate, got %T: %+v", msg, msg)
	}
	// No intermediate Twrite -- Size=0 means remove.
	msg = clunk(t, tc, 5, 2)
	if _, ok := msg.(*proto.Rclunk); !ok {
		t.Fatalf("expected Rclunk on remove, got %T: %+v", msg, msg)
	}

	// Verify removal: Txattrwalk for the removed key returns ENODATA.
	msg = xattrwalk(t, tc, 6, 1, 11, "user.existing")
	expectRlerror(t, msg, proto.ENODATA)
}

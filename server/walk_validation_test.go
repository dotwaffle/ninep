package server

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// spyDir is a directory node that records every Lookup call. It delegates
// child resolution to the embedded Inode but tracks the names it was asked
// to resolve so tests can assert that name validation rejected a request
// BEFORE any Lookup was invoked.
type spyDir struct {
	Inode

	mu      sync.Mutex
	lookups []string
	calls   atomic.Int32
}

func (d *spyDir) Lookup(ctx context.Context, name string) (Node, error) {
	d.mu.Lock()
	d.lookups = append(d.lookups, name)
	d.mu.Unlock()
	d.calls.Add(1)
	return d.Inode.Lookup(ctx, name)
}

// spyTree returns a tree where the root is a spyDir with a single child
// directory "sub" that is also a spyDir. This lets tests walk to
// ["..", "sub"] and similar.
func spyTree() *spyDir {
	sub := &spyDir{}
	sub.Init(proto.QID{Type: proto.QTDIR, Version: 0, Path: 2}, sub)

	root := &spyDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Version: 0, Path: 1}, root)
	root.AddChild("sub", sub.EmbeddedInode())

	return root
}

// TestWalk_RejectsSlashInName verifies that a Twalk whose name element
// contains '/' is rejected with EINVAL before any Lookup is invoked. This
// closes a path-traversal vector against passthrough-backed servers where
// the name would otherwise reach unix.Fstatat verbatim.
func TestWalk_RejectsSlashInName(t *testing.T) {
	t.Parallel()
	root := spyTree()
	cp := newConnPair(t, root)
	defer cp.close(t)
	cp.attach(t, 1, 0, "user", "")

	msg := cp.walk(t, 2, 0, 1, "foo/bar")
	isError(t, msg, proto.EINVAL)
	if got := root.calls.Load(); got != 0 {
		t.Errorf("Lookup was invoked %d times for invalid name; want 0 (validation must run first)", got)
	}
}

// TestWalk_RejectsNULInName verifies that a Twalk with a NUL byte in a name
// element returns EINVAL without invoking Lookup. NUL truncates at the C
// syscall boundary and must never reach the filesystem.
func TestWalk_RejectsNULInName(t *testing.T) {
	t.Parallel()
	root := spyTree()
	cp := newConnPair(t, root)
	defer cp.close(t)
	cp.attach(t, 1, 0, "user", "")

	msg := cp.walk(t, 2, 0, 1, "foo\x00bar")
	isError(t, msg, proto.EINVAL)
	if got := root.calls.Load(); got != 0 {
		t.Errorf("Lookup was invoked %d times for NUL-containing name; want 0", got)
	}
}

// TestWalk_RejectsEmptyName verifies that an empty-string name element is
// rejected with EINVAL.
func TestWalk_RejectsEmptyName(t *testing.T) {
	t.Parallel()
	root := spyTree()
	cp := newConnPair(t, root)
	defer cp.close(t)
	cp.attach(t, 1, 0, "user", "")

	msg := cp.walk(t, 2, 0, 1, "")
	isError(t, msg, proto.EINVAL)
	if got := root.calls.Load(); got != 0 {
		t.Errorf("Lookup was invoked %d times for empty name; want 0", got)
	}
}

// TestWalk_RejectsSlashInSecondElement verifies that validation runs for
// EVERY element, not just the first.
func TestWalk_RejectsSlashInSecondElement(t *testing.T) {
	t.Parallel()
	root := spyTree()
	cp := newConnPair(t, root)
	defer cp.close(t)
	cp.attach(t, 1, 0, "user", "")

	msg := cp.walk(t, 2, 0, 1, "sub", "bad/name")
	isError(t, msg, proto.EINVAL)
}

// TestWalk_AllowsDot verifies that "." is a legal walk element (spec
// compliant: means "current directory").
func TestWalk_AllowsDot(t *testing.T) {
	t.Parallel()
	root := spyTree()
	cp := newConnPair(t, root)
	defer cp.close(t)
	cp.attach(t, 1, 0, "user", "")

	// Walk "." -- Inode.Lookup will return ENOENT for "." unless the tree
	// supplies it; the spec-compliance point here is that validation does
	// NOT reject ".", so the request reaches Lookup.
	cp.walk(t, 2, 0, 1, ".")
	if root.calls.Load() == 0 {
		t.Error("'.' was rejected by validation; validation must permit '.' per 9P spec")
	}
}

// TestWalk_AllowsDotDot verifies that ".." is a legal walk element.
func TestWalk_AllowsDotDot(t *testing.T) {
	t.Parallel()
	root := spyTree()
	cp := newConnPair(t, root)
	defer cp.close(t)
	cp.attach(t, 1, 0, "user", "")

	cp.walk(t, 2, 0, 1, "..")
	if root.calls.Load() == 0 {
		t.Error("'..' was rejected by validation; validation must permit '..' per 9P spec")
	}
}

// TestLcreate_RejectsSlashInName verifies Tlcreate rejects '/' in the name
// before dispatching to NodeCreater. (Existing bridge tests already cover
// this, but included here to document the cross-handler contract.)
func TestLcreate_RejectsSlashInName(t *testing.T) {
	t.Parallel()
	root := spyTree()
	cp := newConnPair(t, root)
	defer cp.close(t)
	cp.attach(t, 1, 0, "user", "")

	sendMessage(t, cp.client, 2, &p9l.Tlcreate{
		Fid:   0,
		Name:  "a/b",
		Flags: 0,
		Mode:  0o644,
		GID:   0,
	})
	_, msg := readResponse(t, cp.client)
	isError(t, msg, proto.EINVAL)
}

// TestMkdir_RejectsNULInName verifies Tmkdir rejects NUL in the name.
func TestMkdir_RejectsNULInName(t *testing.T) {
	t.Parallel()
	root := spyTree()
	cp := newConnPair(t, root)
	defer cp.close(t)
	cp.attach(t, 1, 0, "user", "")

	sendMessage(t, cp.client, 2, &p9l.Tmkdir{
		DirFid: 0,
		Name:   "a\x00b",
		Mode:   0o755,
		GID:    0,
	})
	_, msg := readResponse(t, cp.client)
	isError(t, msg, proto.EINVAL)
}

// TestSymlink_RejectsSlashInName verifies Tsymlink rejects '/' in the NEW
// LINK NAME (not the target -- targets may legitimately contain '/').
func TestSymlink_RejectsSlashInName(t *testing.T) {
	t.Parallel()
	root := spyTree()
	cp := newConnPair(t, root)
	defer cp.close(t)
	cp.attach(t, 1, 0, "user", "")

	sendMessage(t, cp.client, 2, &p9l.Tsymlink{
		DirFid: 0,
		Name:   "has/slash",
		Target: "/valid/absolute/target/path", // target may contain '/'
		GID:    0,
	})
	_, msg := readResponse(t, cp.client)
	isError(t, msg, proto.EINVAL)
}

// TestRename_RejectsSlashInNewName verifies Trename rejects '/' in the new
// name.
func TestRename_RejectsSlashInNewName(t *testing.T) {
	t.Parallel()
	root := spyTree()
	cp := newConnPair(t, root)
	defer cp.close(t)
	cp.attach(t, 1, 0, "user", "")

	// Walk to sub so we have a valid fid to rename.
	cp.walk(t, 2, 0, 1, "sub")

	sendMessage(t, cp.client, 3, &p9l.Trename{
		Fid:    1,
		DirFid: 0,
		Name:   "bad/name",
	})
	_, msg := readResponse(t, cp.client)
	isError(t, msg, proto.EINVAL)
}

// TestMknod_RejectsNULInName verifies Tmknod rejects NUL in the name.
func TestMknod_RejectsNULInName(t *testing.T) {
	t.Parallel()
	root := spyTree()
	cp := newConnPair(t, root)
	defer cp.close(t)
	cp.attach(t, 1, 0, "user", "")

	sendMessage(t, cp.client, 2, &p9l.Tmknod{
		DirFid: 0,
		Name:   "bad\x00",
		Mode:   0o644,
		Major:  0,
		Minor:  0,
		GID:    0,
	})
	_, msg := readResponse(t, cp.client)
	isError(t, msg, proto.EINVAL)
}

// TestLink_RejectsSlashInNewName verifies Tlink rejects '/' in the new
// link name.
func TestLink_RejectsSlashInNewName(t *testing.T) {
	t.Parallel()
	root := spyTree()
	cp := newConnPair(t, root)
	defer cp.close(t)
	cp.attach(t, 1, 0, "user", "")

	// Walk to sub to get a valid target fid for linking.
	cp.walk(t, 2, 0, 1, "sub")

	sendMessage(t, cp.client, 3, &p9l.Tlink{
		DirFid: 0,
		Fid:    1,
		Name:   "bad/name",
	})
	_, msg := readResponse(t, cp.client)
	isError(t, msg, proto.EINVAL)
}

// TestValidatePathElement_Unit provides direct unit coverage of the helper
// as a supplement (not a substitute) for the wire-path tests above.
func TestValidatePathElement_Unit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		wantErr bool
	}{
		{"", true},
		{"foo", false},
		{".", false},
		{"..", false},
		{"foo/bar", true},
		{"/leading", true},
		{"trailing/", true},
		{"foo\x00bar", true},
		{"\x00", true},
		{"normal.txt", false},
		{"with spaces", false},
		{"unicode-\u00e9", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validatePathElement(tc.name)
			if (err != nil) != tc.wantErr {
				t.Errorf("validatePathElement(%q): err=%v, wantErr=%v", tc.name, err, tc.wantErr)
			}
		})
	}
}

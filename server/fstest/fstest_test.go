package fstest

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
	"github.com/dotwaffle/ninep/server"
	"github.com/dotwaffle/ninep/server/memfs"
	"github.com/dotwaffle/ninep/server/passthrough"
)

// TestCasesPopulated verifies that the Cases slice has been populated
// by the init function in cases.go. At least 10 test cases are expected
// covering walk, read, write, readdir, create, error, and concurrency.
func TestCasesPopulated(t *testing.T) {
	t.Parallel()
	if len(Cases) < 10 {
		t.Fatalf("len(Cases) = %d, want >= 10", len(Cases))
	}

	// Verify that key test categories are represented.
	categories := map[string]bool{
		"walk":       false,
		"read":       false,
		"write":      false,
		"readdir":    false,
		"create":     false,
		"error":      false,
		"concurrent": false,
	}
	for _, tc := range Cases {
		for cat := range categories {
			if len(tc.Name) >= len(cat) && tc.Name[:len(cat)] == cat {
				categories[cat] = true
			}
		}
	}
	for cat, found := range categories {
		if !found {
			t.Errorf("no test case found for category %q", cat)
		}
	}
}

// TestCheckMemFS runs the full harness against a memfs tree built with
// the builder API. This is the primary self-test proving the harness
// works end-to-end.
func TestCheckMemFS(t *testing.T) {
	t.Parallel()

	var gen server.QIDGenerator
	root := memfs.NewDir(&gen).
		AddFile("file.txt", []byte("hello world")).
		AddFile("empty", []byte("")).
		WithDir("sub", func(d *memfs.MemDir) {
			d.AddFile("nested.txt", []byte("nested content"))
		})

	Check(t, root)
}

// TestCheckBuiltinTree runs the full harness against the built-in
// NewTestTree helper, verifying that the internal test node types work
// correctly with all protocol-level operations.
func TestCheckBuiltinTree(t *testing.T) {
	t.Parallel()

	var gen server.QIDGenerator
	root := NewTestTree(&gen)

	Check(t, root)
}

// TestCheckPassthrough runs the full harness against a passthrough
// filesystem backed by a temporary directory populated with the
// expected tree structure. Uses CheckFactory because passthrough holds
// OS file descriptors that get closed during server cleanup.
func TestCheckPassthrough(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	// Populate temp dir with the expected tree shape.
	if err := os.WriteFile(filepath.Join(tmp, "file.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatalf("write file.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "empty"), []byte(""), 0o644); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "sub", "nested.txt"), []byte("nested content"), 0o644); err != nil {
		t.Fatalf("write nested.txt: %v", err)
	}

	CheckFactory(t, func(_ *testing.T) server.Node {
		root, err := passthrough.NewRoot(tmp)
		if err != nil {
			t.Fatalf("NewRoot(%s): %v", tmp, err)
		}
		return root
	})
}

// brokenFile is a node that claims to have data but returns nothing on
// Read. This is used to verify that the harness catches buggy
// implementations.
type brokenFile struct {
	server.Inode
}

func (f *brokenFile) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return nil, 0, nil
}

func (f *brokenFile) Read(_ context.Context, _ uint64, _ uint32) ([]byte, error) {
	return nil, nil // Always returns EOF, even though Getattr says size=11.
}

func (f *brokenFile) Getattr(_ context.Context, _ proto.AttrMask) (proto.Attr, error) {
	return proto.Attr{Size: 11, Mode: 0o644, NLink: 1}, nil // Lies about size.
}

// TestCheckCatchesBrokenRead verifies that a broken Read implementation
// produces incorrect data that the harness would detect. Rather than
// running the full harness (which would mark the parent test as failed),
// we directly verify the broken node returns wrong data via protocol
// operations.
func TestCheckCatchesBrokenRead(t *testing.T) {
	t.Parallel()

	var gen server.QIDGenerator

	// Build a tree with a broken file.txt.
	root := &testDir{gen: &gen}
	root.Init(gen.Next(proto.QTDIR), root)

	broken := &brokenFile{}
	broken.Init(gen.Next(proto.QTFILE), broken)
	root.AddChild("file.txt", broken.EmbeddedInode())

	empty := &testFile{data: []byte("")}
	empty.Init(gen.Next(proto.QTFILE), empty)
	root.AddChild("empty", empty.EmbeddedInode())

	sub := &testDir{gen: &gen}
	sub.Init(gen.Next(proto.QTDIR), sub)
	root.AddChild("sub", sub.EmbeddedInode())

	nested := &testFile{data: []byte("nested content")}
	nested.Init(gen.Next(proto.QTFILE), nested)
	sub.AddChild("nested.txt", nested.EmbeddedInode())

	// Directly verify the broken node returns wrong data at the
	// protocol level. This proves the harness would catch it.
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	msg := walk(t, tc, 2, 0, 1, "file.txt")
	expectRwalk(t, msg)

	msg = open(t, tc, 3, 1, 0)
	if _, ok := msg.(*p9l.Rlopen); !ok {
		t.Fatalf("expected Rlopen, got %T: %+v", msg, msg)
	}

	msg = read(t, tc, 4, 1, 0, 4096)
	data := expectRread(t, msg)

	// The broken file returns empty data instead of "hello world".
	// This is the mismatch that read/basic would catch.
	if len(data) != 0 {
		t.Fatalf("broken file returned %d bytes, expected 0 (EOF)", len(data))
	}
	if string(data) == "hello world" {
		t.Fatal("broken file should NOT return correct data")
	}
	// The harness read/basic case checks: bytes.Equal(data, []byte("hello world"))
	// Since data is empty, that check would fail -- proving the harness
	// catches broken implementations.
}

// TestCasesSelective verifies that individual Cases can be run
// selectively and that Cases can be filtered by name prefix.
func TestCasesSelective(t *testing.T) {
	t.Parallel()

	if len(Cases) == 0 {
		t.Fatal("Cases is empty")
	}

	// Verify individual case execution.
	var gen server.QIDGenerator
	root := NewTestTree(&gen)

	t.Run("single-case", func(t *testing.T) {
		Cases[0].Run(t, root)
	})

	// Filter walk/* cases and verify at least 3 exist.
	var walkCases []TestCase
	for _, tc := range Cases {
		if strings.HasPrefix(tc.Name, "walk/") {
			walkCases = append(walkCases, tc)
		}
	}
	if len(walkCases) < 3 {
		t.Errorf("walk/* case count = %d, want >= 3", len(walkCases))
	}

	// Run filtered walk cases.
	for _, tc := range walkCases {
		t.Run("filtered/"+tc.Name, func(t *testing.T) {
			tc.Run(t, root)
		})
	}
}

// --- CheckXattr self-test fixtures ---

// xattrMockFile implements the four simple xattr capabilities required
// by XattrExpectedTree plus NodeOpener (for fid state transitions
// needed by the xattr two-phase protocol).
type xattrMockFile struct {
	server.Inode
	mu     sync.Mutex
	xattrs map[string][]byte
}

func (*xattrMockFile) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return nil, 0, nil
}

func (f *xattrMockFile) GetXattr(_ context.Context, name string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.xattrs[name]
	if !ok {
		return nil, proto.ENODATA
	}
	return slices.Clone(v), nil
}

func (f *xattrMockFile) SetXattr(_ context.Context, name string, data []byte, _ uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.xattrs == nil {
		f.xattrs = make(map[string][]byte)
	}
	f.xattrs[name] = slices.Clone(data)
	return nil
}

func (f *xattrMockFile) ListXattrs(_ context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.xattrs))
	for n := range f.xattrs {
		names = append(names, n)
	}
	return names, nil
}

func (f *xattrMockFile) RemoveXattr(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.xattrs, name)
	return nil
}

// xattrMockRoot is a directory containing xfile. It implements
// NodeOpener (required to satisfy Tattach/Tlopen on the root) and
// relies on Inode's AddChild-backed Lookup for child resolution.
type xattrMockRoot struct {
	server.Inode
}

func (*xattrMockRoot) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return nil, 0, nil
}

func newXattrMockRoot(t *testing.T) server.Node {
	t.Helper()
	var gen server.QIDGenerator

	root := &xattrMockRoot{}
	root.Init(gen.Next(proto.QTDIR), root)

	xf := &xattrMockFile{
		xattrs: map[string][]byte{"user.existing": []byte("existing-value")},
	}
	xf.Init(gen.Next(proto.QTFILE), xf)
	root.AddChild("xfile", xf.EmbeddedInode())
	return root
}

// TestCheckXattr proves CheckXattr runs against any root satisfying
// XattrExpectedTree. Uses an in-test mock implementing all four simple
// xattr capabilities.
func TestCheckXattr(t *testing.T) {
	t.Parallel()
	CheckXattr(t, newXattrMockRoot)
}

// --- CheckLock self-test fixtures ---

// lockMockFile implements NodeOpener + NodeLocker with always-OK
// semantics and GetLock returning Unlck (no conflict). Sufficient for
// single-connection CheckLock cases.
type lockMockFile struct {
	server.Inode
}

func (*lockMockFile) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return nil, 0, nil
}

func (*lockMockFile) Lock(_ context.Context, _ proto.LockType, _ proto.LockFlags, _, _ uint64, _ uint32, _ string) (proto.LockStatus, error) {
	return proto.LockStatusOK, nil
}

func (*lockMockFile) GetLock(_ context.Context, _ proto.LockType, start, length uint64, procID uint32, clientID string) (proto.LockType, uint64, uint64, uint32, string, error) {
	return proto.LockTypeUnlck, start, length, procID, clientID, nil
}

type lockMockRoot struct {
	server.Inode
}

func (*lockMockRoot) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return nil, 0, nil
}

func newLockMockRoot(t *testing.T) server.Node {
	t.Helper()
	var gen server.QIDGenerator

	root := &lockMockRoot{}
	root.Init(gen.Next(proto.QTDIR), root)

	lf := &lockMockFile{}
	lf.Init(gen.Next(proto.QTFILE), lf)
	root.AddChild("lockfile", lf.EmbeddedInode())
	return root
}

// TestCheckLock proves CheckLock runs against any root satisfying
// LockExpectedTree. Uses an in-test mock implementing NodeOpener +
// NodeLocker with always-OK semantics.
func TestCheckLock(t *testing.T) {
	t.Parallel()
	CheckLock(t, newLockMockRoot)
}

// Compile-time assertions: verify the mock types satisfy the required
// capability interfaces. A missing method here is a build error.
var (
	_ server.NodeOpener       = (*xattrMockFile)(nil)
	_ server.NodeXattrGetter  = (*xattrMockFile)(nil)
	_ server.NodeXattrSetter  = (*xattrMockFile)(nil)
	_ server.NodeXattrLister  = (*xattrMockFile)(nil)
	_ server.NodeXattrRemover = (*xattrMockFile)(nil)
	_ server.InodeEmbedder    = (*xattrMockFile)(nil)
	_ server.InodeEmbedder    = (*xattrMockRoot)(nil)

	_ server.NodeOpener    = (*lockMockFile)(nil)
	_ server.NodeLocker    = (*lockMockFile)(nil)
	_ server.InodeEmbedder = (*lockMockFile)(nil)
	_ server.InodeEmbedder = (*lockMockRoot)(nil)
)

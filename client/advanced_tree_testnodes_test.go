package client_test

// Test fixtures shared by advanced_symlink_test.go, advanced_remove_test.go,
// advanced_rename_test.go, advanced_link_test.go, and advanced_mknod_test.go.
//
// memfs.MemDir implements NodeUnlinker + NodeCreater + NodeMkdirer but NOT
// NodeSymlinker / NodeRenamer / NodeLinker / NodeMknoder. The testRUDir /
// testSymlinkDir wrappers embed server.Inode and implement the missing
// capabilities over the Inode child tree so the Wave-2 Conn.* round-trip
// tests exercise every code path against real wire traffic.

import (
	"context"
	"sync"
	"testing"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

// testRUDir implements NodeSymlinker, NodeRenamer, NodeUnlinker, NodeLinker,
// and NodeMknoder. It also supports Readdir so directory walks succeed.
//
// Mirrors rawTestRUDir (raw_advanced_test.go) but kept separate so each
// Wave-2 plan's test file compiles without cross-plan coupling. "RU" =
// Rename+Unlink+Link+Mknod+Symlink (acronym from the planner's wording).
type testRUDir struct {
	server.Inode
	gen *server.QIDGenerator

	mu          sync.Mutex
	lastSymlink string
	lastMknod   string
}

func (d *testRUDir) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return nil, 0, nil
}

func (d *testRUDir) Readdir(_ context.Context) ([]proto.Dirent, error) {
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

func (d *testRUDir) Symlink(_ context.Context, name, target string, _ uint32) (server.Node, error) {
	sym := server.SymlinkTo(d.gen, target)
	d.AddChild(name, sym.EmbeddedInode())
	d.mu.Lock()
	d.lastSymlink = name
	d.mu.Unlock()
	return sym, nil
}

func (d *testRUDir) Rename(_ context.Context, oldName string, newDir server.Node, newName string) error {
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

func (d *testRUDir) Unlink(_ context.Context, name string, flags uint32) error {
	child, ok := d.Children()[name]
	if !ok {
		return proto.ENOENT
	}
	// Match POSIX semantics: AT_REMOVEDIR required for directories.
	isDir := child.QID().Type&proto.QTDIR != 0
	hasFlag := flags&0x200 != 0
	if isDir && !hasFlag {
		return proto.EISDIR
	}
	if !isDir && hasFlag {
		return proto.ENOTDIR
	}
	d.RemoveChild(name)
	return nil
}

func (d *testRUDir) Link(_ context.Context, target server.Node, name string) error {
	te, ok := target.(server.InodeEmbedder)
	if !ok {
		return proto.ENOTSUP
	}
	d.AddChild(name, te.EmbeddedInode())
	return nil
}

func (d *testRUDir) Mknod(_ context.Context, name string, _ proto.FileMode, major, minor, _ uint32) (server.Node, error) {
	dev := server.DeviceNode(d.gen, major, minor)
	d.AddChild(name, dev.EmbeddedInode())
	d.mu.Lock()
	d.lastMknod = name
	d.mu.Unlock()
	return dev, nil
}

// newTestRUDir builds a testRUDir as the filesystem root. Callers then mutate
// the Inode tree with AddChild to seed fixture state before dialing.
func newTestRUDir(tb testing.TB) *testRUDir {
	tb.Helper()
	gen := &server.QIDGenerator{}
	d := &testRUDir{gen: gen}
	d.Init(gen.Next(proto.QTDIR), d)
	return d
}

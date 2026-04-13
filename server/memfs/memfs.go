// Package memfs provides in-memory filesystem node types for use with the
// ninep server. MemFile, MemDir, and StaticFile are standalone types that
// embed server.Inode and implement relevant capability interfaces. Use them
// directly or via the fluent builder API (NewDir) to construct synthetic
// file trees without boilerplate.
package memfs

import (
	"context"
	"sync"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

// sIFDIR is the POSIX S_IFDIR bit for directory mode.
const sIFDIR = 0o040000

// Compile-time assertions for interface compliance.
var (
	_ server.NodeOpener    = (*MemFile)(nil)
	_ server.NodeReader    = (*MemFile)(nil)
	_ server.NodeWriter    = (*MemFile)(nil)
	_ server.NodeGetattrer = (*MemFile)(nil)
	_ server.NodeSetattrer = (*MemFile)(nil)
	_ server.InodeEmbedder = (*MemFile)(nil)

	_ server.NodeOpener    = (*MemDir)(nil)
	_ server.NodeReaddirer = (*MemDir)(nil)
	_ server.NodeGetattrer = (*MemDir)(nil)
	_ server.NodeCreater   = (*MemDir)(nil)
	_ server.NodeMkdirer   = (*MemDir)(nil)
	_ server.NodeUnlinker  = (*MemDir)(nil)
	_ server.InodeEmbedder = (*MemDir)(nil)

	_ server.NodeOpener    = (*StaticFile)(nil)
	_ server.NodeReader    = (*StaticFile)(nil)
	_ server.NodeGetattrer = (*StaticFile)(nil)
	_ server.InodeEmbedder = (*StaticFile)(nil)
)

// MemFile is a read-write in-memory file. It stores data in a byte slice
// protected by a sync.RWMutex for concurrent access. MemFile implements
// NodeOpener, NodeReader, NodeWriter, NodeGetattrer, and NodeSetattrer.
//
// Note: MemFile does not enforce size limits. Production use should wrap
// the NodeWriter with size checking if unbounded growth is a concern.
type MemFile struct {
	server.Inode
	mu   sync.RWMutex
	Data []byte
	Mode uint32 // POSIX permission bits; defaults to 0o644 if zero.
}

// Open implements server.NodeOpener. MemFile does not use per-open state;
// reads and writes go directly to the node.
func (f *MemFile) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return nil, 0, nil
}

// Read implements server.NodeReader. It returns up to count bytes starting
// at offset. Returns nil, nil when offset is at or past the end of data.
func (f *MemFile) Read(_ context.Context, offset uint64, count uint32) ([]byte, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	size := uint64(len(f.Data))
	if offset >= size {
		return nil, nil
	}
	end := min(offset+uint64(count), size)
	// Return a copy to avoid exposing internal state.
	out := make([]byte, end-offset)
	copy(out, f.Data[offset:end])
	return out, nil
}

// Write implements server.NodeWriter. It writes data at offset, extending
// the underlying slice if necessary.
func (f *MemFile) Write(_ context.Context, data []byte, offset uint64) (uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	end := int(offset) + len(data)
	if end > len(f.Data) {
		newData := make([]byte, end)
		copy(newData, f.Data)
		f.Data = newData
	}
	copy(f.Data[offset:], data)
	return uint32(len(data)), nil
}

// Getattr implements server.NodeGetattrer. It returns the file mode
// (defaulting to 0o644), size, and NLink=1.
func (f *MemFile) Getattr(_ context.Context, _ proto.AttrMask) (proto.Attr, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	mode := f.Mode
	if mode == 0 {
		mode = 0o644
	}
	return proto.Attr{
		Mode:  mode,
		Size:  uint64(len(f.Data)),
		NLink: 1,
	}, nil
}

// Setattr implements server.NodeSetattrer. It applies mode and size
// changes when the corresponding bits are set in attr.Valid.
func (f *MemFile) Setattr(_ context.Context, attr proto.SetAttr) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if attr.Valid&proto.SetAttrMode != 0 {
		f.Mode = attr.Mode
	}
	if attr.Valid&proto.SetAttrSize != 0 {
		newSize := int(attr.Size)
		if newSize < len(f.Data) {
			f.Data = f.Data[:newSize]
		} else if newSize > len(f.Data) {
			newData := make([]byte, newSize)
			copy(newData, f.Data)
			f.Data = newData
		}
	}
	return nil
}

// MemDir is an in-memory directory node. It serves directory entries from
// its Inode children and supports Create and Mkdir for dynamic tree
// construction. MemDir implements NodeOpener, NodeReaddirer, NodeGetattrer,
// NodeCreater, NodeMkdirer, and NodeUnlinker.
type MemDir struct {
	server.Inode
	gen  *server.QIDGenerator
	Mode uint32 // POSIX permission bits; defaults to 0o755 if zero.
}

// Open implements server.NodeOpener. MemDir does not use per-open state.
func (d *MemDir) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return nil, 0, nil
}

// Readdir implements server.NodeReaddirer. It returns directory entries
// built from the Inode's children.
func (d *MemDir) Readdir(_ context.Context) ([]proto.Dirent, error) {
	children := d.Children()
	entries := make([]proto.Dirent, 0, len(children))
	var offset uint64
	for name, inode := range children {
		qid := inode.QID()
		offset++
		entries = append(entries, proto.Dirent{
			QID:    qid,
			Offset: offset,
			Type:   uint8(qid.Type),
			Name:   name,
		})
	}
	return entries, nil
}

// Getattr implements server.NodeGetattrer. It returns the directory mode
// (defaulting to S_IFDIR|0o755) and NLink = 2 + number of children.
func (d *MemDir) Getattr(_ context.Context, _ proto.AttrMask) (proto.Attr, error) {
	children := d.Children()
	mode := d.Mode
	if mode == 0 {
		mode = 0o755
	}
	return proto.Attr{
		Mode:  sIFDIR | mode,
		NLink: uint64(2 + len(children)),
	}, nil
}

// Create implements server.NodeCreater. It creates a new MemFile child
// with the given mode and registers it in the Inode tree.
func (d *MemDir) Create(_ context.Context, name string, _ uint32, mode proto.FileMode, _ uint32) (server.Node, server.FileHandle, uint32, error) {
	child := &MemFile{Mode: uint32(mode)}
	child.Init(d.gen.Next(proto.QTFILE), child)
	d.AddChild(name, child.EmbeddedInode())
	return child, nil, 0, nil
}

// Mkdir implements server.NodeMkdirer. It creates a new MemDir child and
// registers it in the Inode tree.
func (d *MemDir) Mkdir(_ context.Context, name string, mode proto.FileMode, _ uint32) (server.Node, error) {
	child := &MemDir{gen: d.gen, Mode: uint32(mode)}
	child.Init(d.gen.Next(proto.QTDIR), child)
	d.AddChild(name, child.EmbeddedInode())
	return child, nil
}

// Unlink implements server.NodeUnlinker. It removes the named entry from
// this directory.
func (d *MemDir) Unlink(_ context.Context, name string, _ uint32) error {
	d.EmbeddedInode().RemoveChild(name)
	return nil
}

// StaticFile is a read-only in-memory file. Its content is a string that
// cannot be modified via Write (which returns ENOSYS from the embedded
// Inode default). StaticFile implements NodeOpener, NodeReader, and
// NodeGetattrer.
type StaticFile struct {
	server.Inode
	Content string
	Mode    uint32 // POSIX permission bits; defaults to 0o444 if zero.
}

// Open implements server.NodeOpener. StaticFile does not use per-open state.
func (f *StaticFile) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return nil, 0, nil
}

// Read implements server.NodeReader. It returns bytes from Content starting
// at offset. Returns nil, nil when offset is at or past the end.
func (f *StaticFile) Read(_ context.Context, offset uint64, count uint32) ([]byte, error) {
	data := []byte(f.Content)
	size := uint64(len(data))
	if offset >= size {
		return nil, nil
	}
	end := min(offset+uint64(count), size)
	out := make([]byte, end-offset)
	copy(out, data[offset:end])
	return out, nil
}

// Getattr implements server.NodeGetattrer. It returns the file mode
// (defaulting to 0o444), size, and NLink=1.
func (f *StaticFile) Getattr(_ context.Context, _ proto.AttrMask) (proto.Attr, error) {
	mode := f.Mode
	if mode == 0 {
		mode = 0o444
	}
	return proto.Attr{
		Mode:  mode,
		Size:  uint64(len(f.Content)),
		NLink: 1,
	}, nil
}

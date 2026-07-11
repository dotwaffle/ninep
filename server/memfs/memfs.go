package memfs

import (
	"context"
	"sync"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

const (
	// sIFDIR is the POSIX S_IFDIR bit for directory mode.
	sIFDIR = 0o040000

	maxMemFileSize = uint64(proto.MaxDataSize)
)

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
// MemFile bounds file contents to proto.MaxDataSize to keep sparse writes and
// truncates from allocating unbounded memory.
type MemFile struct {
	server.Inode
	mu   sync.RWMutex
	data []byte
	mode uint32
	uid  uint32
	gid  uint32
}

// NewFile creates an uninitialized in-memory node with an owned copy of data.
// Call Init before inserting it into a custom inode tree; builder methods do
// this automatically.
func NewFile(data []byte) *MemFile {
	return NewFileWithMode(data, 0o644)
}

// NewFileWithMode is NewFile with explicit POSIX permission bits.
func NewFileWithMode(data []byte, mode uint32) *MemFile {
	return &MemFile{data: append([]byte(nil), data...), mode: mode}
}

// Snapshot returns an owned copy of the current contents.
func (f *MemFile) Snapshot() []byte {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return append([]byte(nil), f.data...)
}

// Replace atomically replaces the file contents with an owned copy.
func (f *MemFile) Replace(data []byte) error {
	if uint64(len(data)) > maxMemFileSize {
		return proto.EFBIG
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data = append(f.data[:0], data...)
	return nil
}

// Open implements server.NodeOpener. MemFile does not use per-open state;
// reads and writes go directly to the node.
func (f *MemFile) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return nil, 0, nil
}

// Read implements server.NodeReader. It copies up to len(buf) bytes starting
// at offset into buf. Returns 0, nil when offset is at or past the end of data.
func (f *MemFile) Read(_ context.Context, buf []byte, offset uint64) (int, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	size := uint64(len(f.data))
	if offset >= size {
		return 0, nil
	}
	end := min(offset+uint64(len(buf)), size)
	return copy(buf, f.data[offset:end]), nil
}

// Write implements server.NodeWriter. It writes data at offset, extending
// the underlying slice if necessary.
func (f *MemFile) Write(_ context.Context, data []byte, offset uint64) (uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	end, err := checkedSize(offset, len(data))
	if err != nil {
		return 0, err
	}
	if end > len(f.data) {
		newData := make([]byte, end)
		copy(newData, f.data)
		f.data = newData
	}
	copy(f.data[offset:], data)
	return uint32(len(data)), nil
}

// Getattr implements server.NodeGetattrer. It returns the file mode
// (defaulting to 0o644), size, and NLink=1.
func (f *MemFile) Getattr(_ context.Context, _ proto.AttrMask) (proto.Attr, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	mode := f.mode
	if mode == 0 {
		mode = 0o644
	}
	return proto.Attr{
		Mode:  mode,
		UID:   f.uid,
		GID:   f.gid,
		Size:  uint64(len(f.data)),
		NLink: 1,
	}, nil
}

// Setattr implements server.NodeSetattrer. It applies mode, size, and
// ownership changes when the corresponding bits are set in attr.Valid.
func (f *MemFile) Setattr(_ context.Context, attr proto.SetAttr) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if attr.Valid&proto.SetAttrSize != 0 && attr.Size > maxMemFileSize {
		return proto.EFBIG
	}
	if attr.Valid&proto.SetAttrMode != 0 {
		f.mode = attr.Mode
	}
	if attr.Valid&proto.SetAttrUID != 0 {
		f.uid = attr.UID
	}
	if attr.Valid&proto.SetAttrGID != 0 {
		f.gid = attr.GID
	}
	if attr.Valid&proto.SetAttrSize != 0 {
		newSize := int(attr.Size)
		if newSize < len(f.data) {
			f.data = f.data[:newSize]
		} else if newSize > len(f.data) {
			newData := make([]byte, newSize)
			copy(newData, f.data)
			f.data = newData
		}
	}
	return nil
}

func checkedSize(offset uint64, count int) (int, error) {
	if offset > maxMemFileSize || uint64(count) > maxMemFileSize-offset {
		return 0, proto.EFBIG
	}
	return int(offset) + count, nil
}

// MemDir is an in-memory directory node. It serves directory entries from
// its Inode children and supports Create and Mkdir for dynamic tree
// construction. MemDir implements NodeOpener, NodeReaddirer, NodeGetattrer,
// NodeCreater, NodeMkdirer, and NodeUnlinker.
type MemDir struct {
	server.Inode
	gen  *server.QIDGenerator
	mode uint32
	uid  uint32
	gid  uint32
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
			Type:   proto.QIDTypeToDT(qid.Type),
			Name:   name,
		})
	}
	return entries, nil
}

// Getattr implements server.NodeGetattrer. It returns the directory mode
// (defaulting to S_IFDIR|0o755) and NLink = 2 + number of children.
func (d *MemDir) Getattr(_ context.Context, _ proto.AttrMask) (proto.Attr, error) {
	children := d.Children()
	mode := d.mode
	if mode == 0 {
		mode = 0o755
	}
	return proto.Attr{
		Mode:  sIFDIR | mode,
		UID:   d.uid,
		GID:   d.gid,
		NLink: uint64(2 + len(children)),
	}, nil
}

// Create implements server.NodeCreater. It creates a new MemFile child
// with the given mode and registers it in the Inode tree atomically via
// TryAddChild. If a child of the same name already exists it returns
// proto.EEXIST, regardless of the open flags, rather than silently
// replacing the entry.
func (d *MemDir) Create(_ context.Context, name string, _ uint32, mode proto.FileMode, _ uint32) (server.Node, server.FileHandle, uint32, error) {
	child := NewFileWithMode(nil, uint32(mode))
	child.Init(d.gen.Next(proto.QTFILE), child)
	if !d.TryAddChild(name, child.EmbeddedInode()) {
		return nil, nil, 0, proto.EEXIST
	}
	return child, nil, 0, nil
}

// Mkdir implements server.NodeMkdirer. It creates a new MemDir child and
// registers it in the Inode tree atomically via TryAddChild. If a child
// of the same name already exists it returns proto.EEXIST rather than
// silently replacing the entry.
func (d *MemDir) Mkdir(_ context.Context, name string, mode proto.FileMode, _ uint32) (server.Node, error) {
	child := &MemDir{gen: d.gen, mode: uint32(mode)}
	child.Init(d.gen.Next(proto.QTDIR), child)
	if !d.TryAddChild(name, child.EmbeddedInode()) {
		return nil, proto.EEXIST
	}
	return child, nil
}

// Unlink implements server.NodeUnlinker. It removes the named entry from
// this directory.
func (d *MemDir) Unlink(_ context.Context, name string, flags uint32) error {
	const removeDir = uint32(0x200)
	if flags != 0 && flags != removeDir {
		return proto.EINVAL
	}
	return d.EmbeddedInode().RemoveChildIf(name, func(node server.Node, childCount int) error {
		isDir := node.QID().Type == proto.QTDIR
		if flags == removeDir {
			if !isDir {
				return proto.ENOTDIR
			}
			if childCount != 0 {
				return proto.ENOTEMPTY
			}
			return nil
		}
		if isDir {
			return proto.EISDIR
		}
		return nil
	})
}

// StaticFile is a read-only in-memory file. Its content is a string that
// cannot be modified via Write (which returns ENOSYS from the embedded
// Inode default). StaticFile implements NodeOpener, NodeReader, and
// NodeGetattrer.
type StaticFile struct {
	server.Inode
	content string
	mode    uint32
}

// NewStaticFile creates an immutable file with default read-only permissions.
func NewStaticFile(content string) *StaticFile {
	return NewStaticFileWithMode(content, 0o444)
}

// NewStaticFileWithMode is NewStaticFile with explicit permission bits.
func NewStaticFileWithMode(content string, mode uint32) *StaticFile {
	return &StaticFile{content: content, mode: mode}
}

// Open implements server.NodeOpener. StaticFile does not use per-open state.
func (f *StaticFile) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return nil, 0, nil
}

// Read implements server.NodeReader. It copies bytes from Content starting
// at offset into buf. Returns 0, nil when offset is at or past the end.
func (f *StaticFile) Read(_ context.Context, buf []byte, offset uint64) (int, error) {
	data := []byte(f.content)
	size := uint64(len(data))
	if offset >= size {
		return 0, nil
	}
	end := min(offset+uint64(len(buf)), size)
	return copy(buf, data[offset:end]), nil
}

// Getattr implements server.NodeGetattrer. It returns the file mode
// (defaulting to 0o444), size, and NLink=1.
func (f *StaticFile) Getattr(_ context.Context, _ proto.AttrMask) (proto.Attr, error) {
	mode := f.mode
	if mode == 0 {
		mode = 0o444
	}
	return proto.Attr{
		Mode:  mode,
		Size:  uint64(len(f.content)),
		NLink: 1,
	}, nil
}

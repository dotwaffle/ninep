package server

import (
	"context"
	"maps"
	"sync"

	"github.com/dotwaffle/ninep/proto"
)

// Inode provides default implementations for all capability interfaces,
// returning ENOSYS for unimplemented operations. Embed *Inode in your
// node struct and call Init to set up the QID and back-reference.
//
// Inode also manages the filesystem tree: parent/child relationships,
// child lookup, and child enumeration.
type Inode struct {
	qid      proto.QID
	parent   *Inode
	children map[string]*Inode
	mu       sync.Mutex
	node     InodeEmbedder // back-reference to the user's struct
}

// Compile-time assertions that *Inode implements all capability interfaces.
var (
	_ InodeEmbedder    = (*Inode)(nil)
	_ NodeOpener       = (*Inode)(nil)
	_ NodeReader       = (*Inode)(nil)
	_ NodeWriter       = (*Inode)(nil)
	_ NodeGetattrer    = (*Inode)(nil)
	_ NodeSetattrer    = (*Inode)(nil)
	_ NodeReaddirer    = (*Inode)(nil)
	_ NodeCreater      = (*Inode)(nil)
	_ NodeMkdirer      = (*Inode)(nil)
	_ NodeCloser       = (*Inode)(nil)
	_ NodeLookuper     = (*Inode)(nil)
	_ NodeSymlinker    = (*Inode)(nil)
	_ NodeLinker       = (*Inode)(nil)
	_ NodeMknoder      = (*Inode)(nil)
	_ NodeReadlinker   = (*Inode)(nil)
	_ NodeUnlinker     = (*Inode)(nil)
	_ NodeRenamer      = (*Inode)(nil)
	_ NodeStatFSer     = (*Inode)(nil)
	_ NodeLocker       = (*Inode)(nil)
	_ NodeXattrGetter  = (*Inode)(nil)
	_ NodeXattrSetter  = (*Inode)(nil)
	_ NodeXattrLister  = (*Inode)(nil)
	_ NodeXattrRemover = (*Inode)(nil)
)

// Init initializes the Inode with a QID and a back-reference to the
// embedding node. If node is nil, the Inode references itself.
func (i *Inode) Init(qid proto.QID, node InodeEmbedder) {
	i.qid = qid
	if node == nil {
		i.node = i
	} else {
		i.node = node
	}
}

// EmbeddedInode returns a pointer to the embedded Inode. Satisfies
// InodeEmbedder.
func (i *Inode) EmbeddedInode() *Inode {
	return i
}

// QID returns the Inode's QID.
func (i *Inode) QID() proto.QID {
	return i.qid
}

// Parent returns the parent Inode, or nil if this is the root.
func (i *Inode) Parent() *Inode {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.parent
}

// AddChild adds a child inode under the given name. The child's parent
// is set to this inode. The children map is lazily initialized.
// Lock ordering: parent lock acquired before child lock to avoid deadlock.
func (i *Inode) AddChild(name string, child *Inode) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.children == nil {
		i.children = make(map[string]*Inode)
	}
	child.mu.Lock()
	child.parent = i
	child.mu.Unlock()
	i.children[name] = child
}

// RemoveChild removes a child by name.
func (i *Inode) RemoveChild(name string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.children, name)
}

// Children returns a snapshot copy of the children map.
func (i *Inode) Children() map[string]*Inode {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.children == nil {
		return nil
	}
	out := make(map[string]*Inode, len(i.children))
	maps.Copy(out, i.children)
	return out
}

// Lookup resolves a child by name. If the child exists, it returns the
// user's node (via the back-reference). If not found, returns proto.ENOENT.
// Users override this by implementing NodeLookuper on their struct.
func (i *Inode) Lookup(_ context.Context, name string) (Node, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	child, ok := i.children[name]
	if !ok {
		return nil, proto.ENOENT
	}
	return child.node.(Node), nil
}

// Open returns (nil, 0, proto.ENOSYS). Override by implementing NodeOpener.
func (i *Inode) Open(_ context.Context, _ uint32) (FileHandle, uint32, error) {
	return nil, 0, proto.ENOSYS
}

// Read returns (nil, proto.ENOSYS). Override by implementing NodeReader.
func (i *Inode) Read(_ context.Context, _ uint64, _ uint32) ([]byte, error) {
	return nil, proto.ENOSYS
}

// Write returns (0, proto.ENOSYS). Override by implementing NodeWriter.
func (i *Inode) Write(_ context.Context, _ []byte, _ uint64) (uint32, error) {
	return 0, proto.ENOSYS
}

// Getattr returns (proto.Attr{}, proto.ENOSYS). Override by implementing NodeGetattrer.
func (i *Inode) Getattr(_ context.Context, _ proto.AttrMask) (proto.Attr, error) {
	return proto.Attr{}, proto.ENOSYS
}

// Setattr returns proto.ENOSYS. Override by implementing NodeSetattrer.
func (i *Inode) Setattr(_ context.Context, _ proto.SetAttr) error {
	return proto.ENOSYS
}

// Readdir returns (nil, proto.ENOSYS). Override by implementing NodeReaddirer.
func (i *Inode) Readdir(_ context.Context) ([]proto.Dirent, error) {
	return nil, proto.ENOSYS
}

// Create returns (nil, nil, 0, proto.ENOSYS). Override by implementing NodeCreater.
func (i *Inode) Create(_ context.Context, _ string, _ uint32, _ proto.FileMode, _ uint32) (Node, FileHandle, uint32, error) {
	return nil, nil, 0, proto.ENOSYS
}

// Mkdir returns (nil, proto.ENOSYS). Override by implementing NodeMkdirer.
func (i *Inode) Mkdir(_ context.Context, _ string, _ proto.FileMode, _ uint32) (Node, error) {
	return nil, proto.ENOSYS
}

// Close is a no-op that returns nil. Override by implementing NodeCloser.
func (i *Inode) Close(_ context.Context) error {
	return nil
}

// Symlink returns (nil, proto.ENOSYS). Override by implementing NodeSymlinker.
func (i *Inode) Symlink(_ context.Context, _, _ string, _ uint32) (Node, error) {
	return nil, proto.ENOSYS
}

// Link returns proto.ENOSYS. Override by implementing NodeLinker.
func (i *Inode) Link(_ context.Context, _ Node, _ string) error {
	return proto.ENOSYS
}

// Mknod returns (nil, proto.ENOSYS). Override by implementing NodeMknoder.
func (i *Inode) Mknod(_ context.Context, _ string, _ proto.FileMode, _, _, _ uint32) (Node, error) {
	return nil, proto.ENOSYS
}

// Readlink returns ("", proto.ENOSYS). Override by implementing NodeReadlinker.
func (i *Inode) Readlink(_ context.Context) (string, error) {
	return "", proto.ENOSYS
}

// Unlink returns proto.ENOSYS. Override by implementing NodeUnlinker.
func (i *Inode) Unlink(_ context.Context, _ string, _ uint32) error {
	return proto.ENOSYS
}

// Rename returns proto.ENOSYS. Override by implementing NodeRenamer.
func (i *Inode) Rename(_ context.Context, _ string, _ Node, _ string) error {
	return proto.ENOSYS
}

// StatFS returns (proto.FSStat{}, proto.ENOSYS). Override by implementing NodeStatFSer.
func (i *Inode) StatFS(_ context.Context) (proto.FSStat, error) {
	return proto.FSStat{}, proto.ENOSYS
}

// Lock returns (0, proto.ENOSYS). Override by implementing NodeLocker.
func (i *Inode) Lock(_ context.Context, _ proto.LockType, _ proto.LockFlags, _, _ uint64, _ uint32, _ string) (proto.LockStatus, error) {
	return 0, proto.ENOSYS
}

// GetLock returns zero values and proto.ENOSYS. Override by implementing NodeLocker.
func (i *Inode) GetLock(_ context.Context, _ proto.LockType, _, _ uint64, _ uint32, _ string) (proto.LockType, uint64, uint64, uint32, string, error) {
	return 0, 0, 0, 0, "", proto.ENOSYS
}

// GetXattr returns (nil, proto.ENOSYS). Override by implementing NodeXattrGetter.
func (i *Inode) GetXattr(_ context.Context, _ string) ([]byte, error) {
	return nil, proto.ENOSYS
}

// SetXattr returns proto.ENOSYS. Override by implementing NodeXattrSetter.
func (i *Inode) SetXattr(_ context.Context, _ string, _ []byte, _ uint32) error {
	return proto.ENOSYS
}

// ListXattrs returns (nil, proto.ENOSYS). Override by implementing NodeXattrLister.
func (i *Inode) ListXattrs(_ context.Context) ([]string, error) {
	return nil, proto.ENOSYS
}

// RemoveXattr returns proto.ENOSYS. Override by implementing NodeXattrRemover.
func (i *Inode) RemoveXattr(_ context.Context, _ string) error {
	return proto.ENOSYS
}

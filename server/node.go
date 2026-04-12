package server

import (
	"context"

	"github.com/dotwaffle/ninep/proto"
)

// Node is the minimal interface every filesystem node must implement.
// For most use cases, embed *Inode instead and implement capability
// interfaces selectively.
type Node interface {
	// QID returns the server's unique identifier for this node.
	QID() proto.QID
}

// InodeEmbedder is the base interface for all filesystem nodes that
// use the Inode tree management. Implement by embedding *Inode in
// your struct and calling Inode.Init during construction.
type InodeEmbedder interface {
	// EmbeddedInode returns the embedded Inode pointer.
	EmbeddedInode() *Inode
}

// NodeLookuper is implemented by directory nodes that can resolve child names.
// Walk calls Lookup for each path element.
type NodeLookuper interface {
	// Lookup resolves a child by name, returning the child Node or an error.
	// Return proto.ENOENT (wrapped) if the name does not exist.
	Lookup(ctx context.Context, name string) (Node, error)
}

// NodeOpener is implemented by nodes that can be opened.
type NodeOpener interface {
	// Open opens the node with the given flags and returns a FileHandle,
	// response flags, and any error.
	Open(ctx context.Context, flags uint32) (FileHandle, uint32, error)
}

// NodeReader is implemented by nodes that support reading.
type NodeReader interface {
	// Read reads up to count bytes starting at offset.
	Read(ctx context.Context, offset uint64, count uint32) ([]byte, error)
}

// NodeWriter is implemented by nodes that support writing.
type NodeWriter interface {
	// Write writes data at the given offset and returns the count of bytes written.
	Write(ctx context.Context, data []byte, offset uint64) (uint32, error)
}

// NodeGetattrer is implemented by nodes that return file attributes.
type NodeGetattrer interface {
	// Getattr retrieves attributes specified by mask.
	Getattr(ctx context.Context, mask proto.AttrMask) (proto.Attr, error)
}

// NodeSetattrer is implemented by nodes that support setting attributes.
type NodeSetattrer interface {
	// Setattr modifies attributes specified in attr.
	Setattr(ctx context.Context, attr proto.SetAttr) error
}

// NodeReaddirer is implemented by directory nodes that return all entries.
// The server handles offset tracking and dirent packing per-fid.
type NodeReaddirer interface {
	// Readdir returns all directory entries. The server caches and packs
	// them into Rreaddir responses using EncodeDirents.
	Readdir(ctx context.Context) ([]proto.Dirent, error)
}

// NodeRawReaddirer is implemented by directory nodes that manage their own
// offset tracking and dirent packing.
type NodeRawReaddirer interface {
	// RawReaddir returns raw dirent bytes for the given offset and count.
	RawReaddir(ctx context.Context, offset uint64, count uint32) ([]byte, error)
}

// NodeCreater is implemented by directory nodes that can create files.
type NodeCreater interface {
	// Create creates a new file in this directory.
	Create(ctx context.Context, name string, flags uint32, mode proto.FileMode, gid uint32) (Node, FileHandle, uint32, error)
}

// NodeMkdirer is implemented by directory nodes that can create subdirectories.
type NodeMkdirer interface {
	// Mkdir creates a new subdirectory in this directory.
	Mkdir(ctx context.Context, name string, mode proto.FileMode, gid uint32) (Node, error)
}

// NodeSymlinker is implemented by directory nodes that can create symbolic links.
type NodeSymlinker interface {
	// Symlink creates a symbolic link named name pointing to target in this
	// directory. Returns the new symlink Node.
	Symlink(ctx context.Context, name, target string, gid uint32) (Node, error)
}

// NodeLinker is implemented by directory nodes that can create hard links.
// The directory receives the request; target is the existing node being linked
// (resolved from Tlink.Fid). Wire format: dfid[4] fid[4] name[s] -- dfid is
// this directory, fid is the target.
type NodeLinker interface {
	// Link creates a hard link named name in this directory pointing to target.
	Link(ctx context.Context, target Node, name string) error
}

// NodeMknoder is implemented by directory nodes that can create device nodes.
type NodeMknoder interface {
	// Mknod creates a device node named name with the given mode, major/minor
	// numbers, and owning group.
	Mknod(ctx context.Context, name string, mode proto.FileMode, major, minor, gid uint32) (Node, error)
}

// NodeReadlinker is implemented by symlink nodes that can report their target.
type NodeReadlinker interface {
	// Readlink returns the target path of this symbolic link.
	Readlink(ctx context.Context) (string, error)
}

// NodeUnlinker is implemented by directory nodes that can remove entries.
// Flags may include AT_REMOVEDIR (0x200) to indicate directory removal.
type NodeUnlinker interface {
	// Unlink removes the entry named name from this directory.
	Unlink(ctx context.Context, name string, flags uint32) error
}

// NodeRenamer is implemented by directory nodes that support renaming entries.
// newDir is the target directory Node resolved from the new directory fid.
type NodeRenamer interface {
	// Rename moves the entry oldName from this directory to newDir with newName.
	Rename(ctx context.Context, oldName string, newDir Node, newName string) error
}

// NodeStatFSer is implemented by nodes that can report filesystem statistics.
type NodeStatFSer interface {
	// StatFS returns filesystem statistics for the filesystem containing this node.
	StatFS(ctx context.Context) (proto.FSStat, error)
}

// NodeLocker is implemented by nodes that support POSIX byte-range locking.
// Implementations control blocking behavior; the library does not impose any
// blocking policy. Implementations should respect context deadlines if blocking.
type NodeLocker interface {
	// Lock acquires, tests, or releases a POSIX byte-range lock.
	Lock(ctx context.Context, lockType proto.LockType, flags proto.LockFlags, start, length uint64, procID uint32, clientID string) (proto.LockStatus, error)
	// GetLock tests whether a lock could be placed, returning the conflicting
	// lock parameters if one exists.
	GetLock(ctx context.Context, lockType proto.LockType, start, length uint64, procID uint32, clientID string) (proto.LockType, uint64, uint64, uint32, string, error)
}

// QIDer is implemented by nodes that provide their own QID. When present,
// nodeQID uses this in preference to Inode.QID.
type QIDer interface {
	// QID returns the node's unique identifier.
	QID() proto.QID
}

// NodeCloser is implemented by nodes that need cleanup on clunk.
type NodeCloser interface {
	// Close releases resources associated with this node.
	Close(ctx context.Context) error
}

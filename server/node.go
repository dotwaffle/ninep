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

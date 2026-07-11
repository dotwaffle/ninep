package server

import (
	"context"

	"github.com/dotwaffle/ninep/proto"
)

// FileHandle is a marker type for per-open state returned by NodeOpener.Open
// (and NodeCreater.Create). Implement FileReader, FileWriter, FileReleaser,
// FileSyncer, FileReaddirer, or FileRawReaddirer on the returned value to
// handle the corresponding wire operations.
//
// A nil FileHandle is permitted for nodes that don't need per-open state;
// the server will not invoke File-level interfaces against a nil handle and
// instead falls back to Node-level capability dispatch on the underlying node.
//
// Footgun: because FileHandle is `any`, the compiler cannot tell that a
// non-nil handle satisfies at least one File* interface. A handle that
// implements none of them is functionally equivalent to nil -- the bridge
// type-asserts each capability and, finding none match, falls through to
// the Node-level path silently. Consumers expecting handle-scoped dispatch
// can guard against typos with a small compile-time assertion in their
// package, e.g.:
//
//	var (
//	    _ server.FileReader   = (*myHandle)(nil)
//	    _ server.FileReleaser = (*myHandle)(nil)
//	)
type FileHandle any

// FileReader is implemented by file handles that support reading.
type FileReader interface {
	// Read reads up to len(buf) bytes starting at offset into buf and
	// returns the number of bytes read. The caller provides a buffer
	// sized to the 9P Tread count (clamped to msize); implementations
	// fill it and return n.
	Read(ctx context.Context, buf []byte, offset uint64) (int, error)
}

// FileWriter is implemented by file handles that support writing.
type FileWriter interface {
	// Write writes data at the given offset and returns the count of bytes written.
	Write(ctx context.Context, data []byte, offset uint64) (uint32, error)
}

// FileReleaser is implemented by file handles that need cleanup on clunk.
type FileReleaser interface {
	// Release releases resources associated with this file handle.
	Release(ctx context.Context) error
}

// FileSyncer is implemented by file handles that support flushing buffered
// writes on the open handle to durable storage. Checked before NodeFsyncer
// by the bridge: Tfsync on an opened fid with a handle that implements
// FileSyncer takes the handle path; only if the handle does not implement
// FileSyncer does the bridge fall back to NodeFsyncer on the underlying
// node.
type FileSyncer interface {
	// Fsync flushes pending writes on the open file to durable storage.
	Fsync(ctx context.Context) error
}

// FileLocker is implemented by file handles that support POSIX byte-range
// locking on the open file. Checked before NodeLocker by the bridge: Tlock
// and Tgetlock on an opened fid with a handle that implements FileLocker
// take the handle path; only if the handle does not implement FileLocker
// does the bridge fall back to NodeLocker on the underlying node.
//
// Prefer this over NodeLocker for filesystems backed by OS file
// descriptors: POSIX record locks are dropped when any descriptor for the
// file is closed by the process, so a lock taken on a transient fd is lost
// the moment that fd closes. The open handle's descriptor lives exactly as
// long as the fid stays open, giving the lock the lifetime the client
// expects. Like NodeLocker, Lock and GetLock are two halves of the same
// Tlock/Tgetlock pair and are always co-implemented.
type FileLocker interface {
	// Lock acquires, tests, or releases a POSIX byte-range lock.
	Lock(ctx context.Context, lockType proto.LockType, flags proto.LockFlags, start, length uint64, procID uint32, clientID string) (proto.LockStatus, error)
	// GetLock tests whether a lock could be placed, returning the conflicting
	// lock parameters if one exists.
	GetLock(ctx context.Context, lockType proto.LockType, start, length uint64, procID uint32, clientID string) (proto.LockType, uint64, uint64, uint32, string, error)
}

// FileReaddirer is implemented by file handles that support reading directory entries.
type FileReaddirer interface {
	// Readdir returns all directory entries for the open handle.
	Readdir(ctx context.Context) ([]proto.Dirent, error)
}

// FileRawReaddirer is implemented by file handles that manage their own
// readdir offset tracking.
type FileRawReaddirer interface {
	// RawReaddir reads raw dirent bytes for the given offset into buf
	// and returns the number of bytes read. The caller provides a buffer
	// sized to the 9P Treaddir count (clamped to msize).
	RawReaddir(ctx context.Context, buf []byte, offset uint64) (int, error)
}

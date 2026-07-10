//go:build linux || freebsd

package passthrough

import (
	"github.com/dotwaffle/ninep/server"
)

// Node represents a file or directory in the passthrough filesystem. It holds
// an OS file descriptor and delegates all operations to the host OS via *at
// syscalls. For directories, the fd is opened with O_RDONLY|O_DIRECTORY. For
// other files, the fd is opened with oPath|O_NOFOLLOW.
//
// parentFd and name are stored for nodes that need parent-anchored *at calls
// (Readlink, Link, Setattr Lchown/UtimesNanoAt) and for platforms that cannot
// reopen a file from the held descriptor.
type Node struct {
	server.Inode
	fd       int
	root     *Root
	parentFd int    // parent directory fd, for *at calls
	name     string // entry name in parent, for *at calls

	// dev is the device number this node's file lived on at Lookup/create
	// time. FreeBSD's openResolved (reopen_freebsd.go) has no /proc/self/fd
	// equivalent to reopen a stale fd directly, so it falls back to
	// re-resolving parentFd+name; dev (together with QID().Path, which
	// already holds the inode number) lets it detect a concurrent
	// rename/symlink swap of that directory entry instead of silently
	// operating on whatever now occupies the name.
	dev uint64
}

// Root is the top-level node of a passthrough filesystem. It wraps a Node
// with configuration (host path, UID mapper). Create with NewRoot.
//
// dev and ino record the export root directory's identity so Lookup can clamp
// ".." at the root: any node whose directory matches (dev, ino) resolves ".."
// to itself rather than the host parent, preventing a walk above the export.
type Root struct {
	Node
	hostPath    string
	mapper      UIDMapper
	dev         uint64
	ino         uint64
	allowDevice bool
}

// Option configures a Root. Pass to NewRoot.
type Option func(*Root)

// WithUIDMapper sets a custom UID/GID mapper for the passthrough filesystem.
// By default, IdentityMapper is used.
func WithUIDMapper(m UIDMapper) Option {
	return func(r *Root) { r.mapper = m }
}

// WithDeviceNodes permits clients to create block and character device nodes
// via Tmknod. It is disabled by default: a privileged server (CAP_MKNOD,
// commonly root) would otherwise let a remote peer create arbitrary device
// nodes inside the export and open them for raw host device access. Enable
// this only when the export is trusted to receive device nodes.
func WithDeviceNodes() Option {
	return func(r *Root) { r.allowDevice = true }
}

// fileHandle wraps an OS file descriptor for per-open read/write operations
// using Pread/Pwrite for offset-based I/O without shared seek position.
type fileHandle struct {
	fd int
}

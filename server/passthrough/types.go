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
// (Readlink, Link, Setattr Lchown/UtimesNanoAt) so the Linux /proc/self/fd
// trick can be replaced with portable *at syscalls.
type Node struct {
	server.Inode
	fd       int
	root     *Root
	parentFd int    // parent directory fd, for *at calls
	name     string // entry name in parent, for *at calls
}

// Root is the top-level node of a passthrough filesystem. It wraps a Node
// with configuration (host path, UID mapper). Create with NewRoot.
type Root struct {
	Node
	hostPath string
	mapper   UIDMapper
}

// Option configures a Root. Pass to NewRoot.
type Option func(*Root)

// WithUIDMapper sets a custom UID/GID mapper for the passthrough filesystem.
// By default, IdentityMapper is used.
func WithUIDMapper(m UIDMapper) Option {
	return func(r *Root) { r.mapper = m }
}

// fileHandle wraps an OS file descriptor for per-open read/write operations
// using Pread/Pwrite for offset-based I/O without shared seek position.
type fileHandle struct {
	fd int
}

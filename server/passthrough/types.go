//go:build linux || freebsd

package passthrough

import (
	"sync"

	"golang.org/x/sys/unix"

	"github.com/dotwaffle/ninep/server"
)

// Node represents a file or directory in the passthrough filesystem. It holds
// an OS file descriptor and delegates all operations to the host OS via *at
// syscalls. For directories, the fd is opened with O_RDONLY|O_DIRECTORY. For
// other files, the fd is opened with oPath|O_NOFOLLOW.
//
// parentFd and name support FreeBSD operations that cannot be issued against
// O_PATH directly. Those fallbacks verify the named entry against the held
// descriptor's device and inode. parentFd is the node's own
// duplicate of the parent directory's fd (see childNode), not the parent
// node's fd number: the parent node can be clunked -- closing its fd -- while
// this node lives on, and a borrowed number would then be stale or, worse,
// silently reused for an unrelated file. Zero means "no parent anchor"
// (root nodes).
type Node struct {
	server.Inode
	fd       int
	root     *Root
	parentFd int    // owned dup of parent directory fd, for *at calls; 0 for root
	name     string // entry name in parent, for *at calls

	// dev and ino identify the held host inode for name-based FreeBSD
	// fallbacks. They are independent of the root-scoped QID path.
	dev uint64
	ino uint64
}

// Root is the top-level node of a passthrough filesystem. It wraps a Node
// with its UID mapper and security options. Create with NewRoot.
//
// dev and ino record the export root directory's identity so Lookup can clamp
// ".." at the root: any node whose directory matches (dev, ino) resolves ".."
// to itself rather than the host parent, preventing a walk above the export.
type Root struct {
	Node
	mapper      UIDMapper
	dev         uint64
	ino         uint64
	allowDevice bool
	qidMu       sync.Mutex
	qidPaths    map[fileIdentity]uint64
	nextQIDPath uint64
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

type dirHandle struct {
	fd int
}

// childNode builds a child Node anchored to directory n, taking ownership
// of fd. The parent anchor is a fresh duplicate of n's directory fd, so the
// child's parent-anchored operations (Setattr chown/utimes, Readlink, the
// FreeBSD reopen fallback) keep working after the parent node is clunked
// and its own fd closed. The duplicate is released in Node.Close. On error
// fd is closed and ownership returns to nobody.
func (n *Node) childNode(fd int, name string, dev, ino uint64) (*Node, error) {
	pfd, err := unix.FcntlInt(uintptr(n.fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return &Node{fd: fd, root: n.root, parentFd: pfd, name: name, dev: dev, ino: ino}, nil
}

// xattrFd opens a short-lived real fd for xattr syscalls via openResolved.
// The node fd is O_PATH for regular files, and the fd-based xattr syscalls
// reject O_PATH descriptors with EBADF. The bridge snapshots xattr data per
// operation (Txattrwalk reads the whole value up front; Tclunk commits
// writes), so a transient fd is sufficient. Callers must close the
// returned fd.
func (n *Node) xattrFd() (int, error) {
	return n.openResolved(unix.O_RDONLY)
}

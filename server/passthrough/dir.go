//go:build linux || freebsd

package passthrough

import (
	"context"

	"golang.org/x/sys/unix"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

// Link creates a hard link named name in this directory pointing to target.
func (n *Node) Link(_ context.Context, target server.Node, name string) error {
	if n.QID().Type != proto.QTDIR {
		return proto.ENOTDIR
	}

	var source *Node
	switch t := target.(type) {
	case *Node:
		source = t
	case *Root:
		source = &t.Node
	default:
		return proto.EINVAL
	}
	if err := source.linkResolved(n.fd, name); err != nil {
		return toProtoErr(err)
	}
	return nil
}

// dtypeToQIDType maps a d_type value to proto.QIDType. The DT_* values
// are identical on Linux and FreeBSD, so this one mapping serves both
// platforms' parseDirents even though the dirent binary layouts they
// parse are not shared.
func dtypeToQIDType(dtype uint8) proto.QIDType {
	switch dtype {
	case unix.DT_DIR:
		return proto.QTDIR
	case unix.DT_LNK:
		return proto.QTSYMLINK
	default:
		return proto.QTFILE
	}
}

// Compile-time interface assertions for directory operations on Node.
var (
	_ server.NodeLookuper   = (*Node)(nil)
	_ server.NodeReaddirer  = (*Node)(nil)
	_ server.NodeCreater    = (*Node)(nil)
	_ server.NodeMkdirer    = (*Node)(nil)
	_ server.NodeSymlinker  = (*Node)(nil)
	_ server.NodeLinker     = (*Node)(nil)
	_ server.NodeMknoder    = (*Node)(nil)
	_ server.NodeReadlinker = (*Node)(nil)
	_ server.NodeUnlinker   = (*Node)(nil)
	_ server.NodeRenamer    = (*Node)(nil)
)

// Lookup resolves a child by name using Fstatat on the directory fd.
// For directories, opens with O_RDONLY|O_DIRECTORY. For symlinks and
// other files, opens with oPath|O_NOFOLLOW.
func (n *Node) Lookup(_ context.Context, name string) (server.Node, error) {
	if name == ".." {
		return n.lookupParent()
	}

	var st unix.Stat_t
	if err := unix.Fstatat(n.fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, toProtoErr(err)
	}

	var fd int
	var err error
	switch uint32(st.Mode) & unix.S_IFMT {
	case unix.S_IFDIR:
		fd, err = unix.Openat(n.fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	default:
		// oPath for non-directories (files, symlinks, devices, etc.).
		fd, err = unix.Openat(n.fd, name, oPath|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return nil, toProtoErr(err)
	}
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, toProtoErr(err)
	}

	child, err := n.childNode(fd, name, uint64(st.Dev))
	if err != nil {
		return nil, toProtoErr(err)
	}
	child.Init(statToQID(&st), child)
	// SetPrunable opts this child into the server's fid-refcounted pruning:
	// passthrough's Lookup always re-resolves from the host filesystem (it
	// never consults the children map as a forward-path cache), so once no
	// fid references the child, the server can safely drop its entry from
	// the parent's children map instead of retaining it for the parent's
	// entire lifetime.
	child.EmbeddedInode().SetPrunable()
	// The child is recorded in the parent's Inode tree so the server can map
	// names to nodes for Trename/Trenameat and prune the entry once its
	// fid refcount drops to zero.
	n.EmbeddedInode().AddChild(name, child.EmbeddedInode())

	return child, nil
}

// Create creates a new file in this directory via Openat with O_CREAT.
// Returns the new Node and a fileHandle for the opened file.
func (n *Node) Create(_ context.Context, name string, flags uint32, mode proto.FileMode, _ uint32) (server.Node, server.FileHandle, uint32, error) {
	if n.QID().Type != proto.QTDIR {
		return nil, nil, 0, proto.ENOTDIR
	}

	fd, err := unix.Openat(n.fd, name, int(flags)|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode))
	if err != nil {
		return nil, nil, 0, toProtoErr(err)
	}

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, nil, 0, toProtoErr(err)
	}

	// Duplicate the descriptor that created the file so the Node and handle
	// cannot diverge if the directory entry is concurrently replaced.
	pathFd, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		_ = unix.Close(fd)
		return nil, nil, 0, toProtoErr(err)
	}

	child, err := n.childNode(pathFd, name, uint64(st.Dev))
	if err != nil {
		_ = unix.Close(fd)
		return nil, nil, 0, toProtoErr(err)
	}
	child.Init(statToQID(&st), child)
	child.EmbeddedInode().SetPrunable()

	return child, &fileHandle{fd: fd}, 0, nil
}

// Mkdir creates a new subdirectory in this directory via Mkdirat.
func (n *Node) Mkdir(_ context.Context, name string, mode proto.FileMode, _ uint32) (server.Node, error) {
	if n.QID().Type != proto.QTDIR {
		return nil, proto.ENOTDIR
	}

	if err := unix.Mkdirat(n.fd, name, uint32(mode)); err != nil {
		return nil, toProtoErr(err)
	}

	fd, err := unix.Openat(n.fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, toProtoErr(err)
	}

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, toProtoErr(err)
	}

	child, err := n.childNode(fd, name, uint64(st.Dev))
	if err != nil {
		return nil, toProtoErr(err)
	}
	child.Init(statToQID(&st), child)
	child.EmbeddedInode().SetPrunable()

	return child, nil
}

// Symlink creates a symbolic link named name pointing to target via Symlinkat.
func (n *Node) Symlink(_ context.Context, name, target string, _ uint32) (server.Node, error) {
	if n.QID().Type != proto.QTDIR {
		return nil, proto.ENOTDIR
	}

	if err := unix.Symlinkat(target, n.fd, name); err != nil {
		return nil, toProtoErr(err)
	}

	fd, err := unix.Openat(n.fd, name, oPath|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, toProtoErr(err)
	}

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, toProtoErr(err)
	}

	child, err := n.childNode(fd, name, uint64(st.Dev))
	if err != nil {
		return nil, toProtoErr(err)
	}
	child.Init(statToQID(&st), child)
	child.EmbeddedInode().SetPrunable()

	return child, nil
}

// Mknod creates a device node named name via mknodat (a per-platform shim:
// the dev argument is int on Linux and uint64 on FreeBSD).
func (n *Node) Mknod(_ context.Context, name string, mode proto.FileMode, major, minor, _ uint32) (server.Node, error) {
	if n.QID().Type != proto.QTDIR {
		return nil, proto.ENOTDIR
	}
	if n.deviceNodeDenied(mode) {
		return nil, proto.EPERM
	}

	if err := mknodat(n.fd, name, uint32(mode), unix.Mkdev(major, minor)); err != nil {
		return nil, toProtoErr(err)
	}

	fd, err := unix.Openat(n.fd, name, oPath|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, toProtoErr(err)
	}

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, toProtoErr(err)
	}

	child, err := n.childNode(fd, name, uint64(st.Dev))
	if err != nil {
		return nil, toProtoErr(err)
	}
	child.Init(statToQID(&st), child)
	child.EmbeddedInode().SetPrunable()

	return child, nil
}

func (n *Node) Readlink(_ context.Context) (string, error) {
	target, err := n.readlinkResolved()
	return target, toProtoErr(err)
}

// Unlink removes the entry named name from this directory via Unlinkat.
func (n *Node) Unlink(_ context.Context, name string, flags uint32) error {
	if n.QID().Type != proto.QTDIR {
		return proto.ENOTDIR
	}

	if err := unix.Unlinkat(n.fd, name, int(flags)); err != nil {
		return toProtoErr(err)
	}

	n.EmbeddedInode().RemoveChild(name)
	return nil
}

// Rename moves the entry oldName from this directory to newDir with newName
// via Renameat.
func (n *Node) Rename(_ context.Context, oldName string, newDir server.Node, newName string) error {
	if n.QID().Type != proto.QTDIR {
		return proto.ENOTDIR
	}

	var newDirFd int
	switch d := newDir.(type) {
	case *Node:
		newDirFd = d.fd
	case *Root:
		newDirFd = d.fd
	default:
		return proto.EINVAL
	}

	if err := unix.Renameat(n.fd, oldName, newDirFd, newName); err != nil {
		return toProtoErr(err)
	}

	return nil
}

// Readdir returns all directory entries. A fresh file descriptor is opened
// for each readdir call to avoid offset issues. On FreeBSD unix.Getdents
// wraps Getdirentries. Only parseDirents differs per platform: the raw
// dirent layouts are not shared.
func (n *Node) Readdir(_ context.Context) ([]proto.Dirent, error) {
	if n.QID().Type != proto.QTDIR {
		return nil, proto.ENOTDIR
	}

	// Open a fresh fd to read directory entries from offset 0.
	fd, err := unix.Openat(n.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, toProtoErr(err)
	}
	defer func() { _ = unix.Close(fd) }()

	var dirents []proto.Dirent
	buf := make([]byte, 8192)

	for {
		nbytes, err := unix.Getdents(fd, buf)
		if err != nil {
			return nil, toProtoErr(err)
		}
		if nbytes == 0 {
			break
		}

		entries := parseDirents(buf[:nbytes])
		dirents = append(dirents, entries...)
	}

	return dirents, nil
}

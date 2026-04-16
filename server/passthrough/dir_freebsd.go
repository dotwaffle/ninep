//go:build freebsd

package passthrough

import (
	"context"
	"encoding/binary"

	"golang.org/x/sys/unix"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

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
// For directories, opens with O_RDONLY|O_DIRECTORY. For symlinks and other
// files, opens with oPath|O_NOFOLLOW.
func (n *Node) Lookup(_ context.Context, name string) (server.Node, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(n.fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, toProtoErr(err)
	}

	var fd int
	var err error
	switch uint32(st.Mode) & unix.S_IFMT {
	case unix.S_IFDIR:
		fd, err = unix.Openat(n.fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	default:
		fd, err = unix.Openat(n.fd, name, oPath|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		return nil, toProtoErr(err)
	}

	child := &Node{fd: fd, root: n.root, parentFd: n.fd, name: name}
	child.Init(statToQID(&st), child)
	n.EmbeddedInode().AddChild(name, child.EmbeddedInode())

	return child, nil
}

// Create creates a new file in this directory via Openat with O_CREAT.
// Returns the new Node and a fileHandle for the opened file.
func (n *Node) Create(_ context.Context, name string, flags uint32, mode proto.FileMode, _ uint32) (server.Node, server.FileHandle, uint32, error) {
	if n.QID().Type != proto.QTDIR {
		return nil, nil, 0, proto.ENOTDIR
	}

	fd, err := unix.Openat(n.fd, name, int(flags)|unix.O_CREAT|unix.O_NOFOLLOW, uint32(mode))
	if err != nil {
		return nil, nil, 0, toProtoErr(err)
	}

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, nil, 0, toProtoErr(err)
	}

	// Open an oPath fd for the node reference, use the real fd for the handle.
	pathFd, err := unix.Openat(n.fd, name, oPath|unix.O_NOFOLLOW, 0)
	if err != nil {
		_ = unix.Close(fd)
		return nil, nil, 0, toProtoErr(err)
	}

	child := &Node{fd: pathFd, root: n.root, parentFd: n.fd, name: name}
	child.Init(statToQID(&st), child)

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

	fd, err := unix.Openat(n.fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, toProtoErr(err)
	}

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, toProtoErr(err)
	}

	child := &Node{fd: fd, root: n.root, parentFd: n.fd, name: name}
	child.Init(statToQID(&st), child)

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

	fd, err := unix.Openat(n.fd, name, oPath|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, toProtoErr(err)
	}

	var st unix.Stat_t
	if err := unix.Fstatat(n.fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		_ = unix.Close(fd)
		return nil, toProtoErr(err)
	}

	child := &Node{fd: fd, root: n.root, parentFd: n.fd, name: name}
	child.Init(statToQID(&st), child)

	return child, nil
}

// Mknod creates a device node named name via Mknodat. FreeBSD's Mknodat
// signature takes dev as uint64 (vs Linux's int).
func (n *Node) Mknod(_ context.Context, name string, mode proto.FileMode, major, minor, _ uint32) (server.Node, error) {
	if n.QID().Type != proto.QTDIR {
		return nil, proto.ENOTDIR
	}

	dev := unix.Mkdev(major, minor)
	if err := unix.Mknodat(n.fd, name, uint32(mode), uint64(dev)); err != nil {
		return nil, toProtoErr(err)
	}

	fd, err := unix.Openat(n.fd, name, oPath|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, toProtoErr(err)
	}

	var st unix.Stat_t
	if err := unix.Fstatat(n.fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		_ = unix.Close(fd)
		return nil, toProtoErr(err)
	}

	child := &Node{fd: fd, root: n.root, parentFd: n.fd, name: name}
	child.Init(statToQID(&st), child)

	return child, nil
}

// Readlink returns the symlink target using Readlinkat on the parent
// directory fd with the entry name.
func (n *Node) Readlink(_ context.Context) (string, error) {
	if n.parentFd == 0 && n.name == "" {
		return "", proto.EINVAL
	}
	buf := make([]byte, 4096)
	nn, err := unix.Readlinkat(n.parentFd, n.name, buf)
	if err != nil {
		return "", toProtoErr(err)
	}
	return string(buf[:nn]), nil
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
// for each readdir call to avoid offset issues. unix.Getdents on FreeBSD
// wraps Getdirentries.
func (n *Node) Readdir(_ context.Context) ([]proto.Dirent, error) {
	if n.QID().Type != proto.QTDIR {
		return nil, proto.ENOTDIR
	}

	fd, err := unix.Openat(n.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY, 0)
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

// parseDirents parses raw FreeBSD getdirentries output into proto.Dirent
// entries. Skips "." and "..".
//
// FreeBSD struct dirent (24-byte header before name):
//
//	Fileno uint64    // bytes 0..7
//	Off    int64     // bytes 8..15
//	Reclen uint16    // bytes 16..17
//	Type   uint8     // byte 18
//	Pad0   uint8     // byte 19
//	Namlen uint16    // bytes 20..21
//	Pad1   uint16    // bytes 22..23
//	Name   variable  // byte 24..
//
// Verified against
// /home/dotwaffle/go/pkg/mod/golang.org/x/sys@v0.42.0/unix/ztypes_freebsd_amd64.go.
func parseDirents(buf []byte) []proto.Dirent {
	const headerLen = 24
	var dirents []proto.Dirent

	for len(buf) >= headerLen {
		ino := binary.LittleEndian.Uint64(buf[0:8])
		reclen := binary.LittleEndian.Uint16(buf[16:18])
		dtype := buf[18]
		namlen := binary.LittleEndian.Uint16(buf[20:22])

		if int(reclen) > len(buf) || reclen < headerLen {
			break
		}
		if int(namlen) > int(reclen)-headerLen {
			break
		}

		name := string(buf[headerLen : headerLen+int(namlen)])
		if name != "." && name != ".." {
			dirents = append(dirents, proto.Dirent{
				QID: proto.QID{
					Type: dtypeToQIDType(dtype),
					Path: ino,
				},
				Type: dtype,
				Name: name,
			})
		}
		buf = buf[reclen:]
	}

	return dirents
}

// dtypeToQIDType maps a d_type to proto.QIDType. The DT_* values are the
// same on FreeBSD as on Linux.
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

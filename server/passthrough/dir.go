package passthrough

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"

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
// For directories, opens with O_RDONLY|O_DIRECTORY. For symlinks and
// other files, opens with O_PATH|O_NOFOLLOW.
func (n *Node) Lookup(_ context.Context, name string) (server.Node, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(n.fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, toProtoErr(err)
	}

	var fd int
	var err error
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		fd, err = unix.Openat(n.fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	default:
		// O_PATH for non-directories (files, symlinks, devices, etc.).
		fd, err = unix.Openat(n.fd, name, unix.O_PATH|unix.O_NOFOLLOW, 0)
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

	// Open an O_PATH fd for the node reference, use the real fd for the handle.
	pathFd, err := unix.Openat(n.fd, name, unix.O_PATH|unix.O_NOFOLLOW, 0)
	if err != nil {
		_ = unix.Close(fd)
		return nil, nil, 0, toProtoErr(err)
	}

	child := &Node{fd: pathFd, root: n.root}
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

	child := &Node{fd: fd, root: n.root}
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

	fd, err := unix.Openat(n.fd, name, unix.O_PATH|unix.O_NOFOLLOW, 0)
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

// Link creates a hard link named name in this directory pointing to target.
func (n *Node) Link(_ context.Context, target server.Node, name string) error {
	if n.QID().Type != proto.QTDIR {
		return proto.ENOTDIR
	}

	targetNode, ok := target.(*Node)
	if !ok {
		// Try Root type.
		if targetRoot, ok := target.(*Root); ok {
			targetNode = &targetRoot.Node
		} else {
			return proto.EINVAL
		}
	}

	// Use AT_EMPTY_PATH with /proc/self/fd/N to link by fd.
	procPath := fmt.Sprintf("/proc/self/fd/%d", targetNode.fd)
	if err := unix.Linkat(unix.AT_FDCWD, procPath, n.fd, name, unix.AT_SYMLINK_FOLLOW); err != nil {
		return toProtoErr(err)
	}

	return nil
}

// Mknod creates a device node named name via Mknodat.
func (n *Node) Mknod(_ context.Context, name string, mode proto.FileMode, major, minor, _ uint32) (server.Node, error) {
	if n.QID().Type != proto.QTDIR {
		return nil, proto.ENOTDIR
	}

	dev := unix.Mkdev(major, minor)
	if err := unix.Mknodat(n.fd, name, uint32(mode), int(dev)); err != nil {
		return nil, toProtoErr(err)
	}

	fd, err := unix.Openat(n.fd, name, unix.O_PATH|unix.O_NOFOLLOW, 0)
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
// directory fd with the entry name. This reads the actual symlink target
// rather than the path the fd resolves to.
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
// for each readdir call to avoid offset issues.
func (n *Node) Readdir(_ context.Context) ([]proto.Dirent, error) {
	if n.QID().Type != proto.QTDIR {
		return nil, proto.ENOTDIR
	}

	// Open a fresh fd to read directory entries from offset 0.
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

// parseDirents parses raw getdents64 output into proto.Dirent entries.
// Skips "." and ".." entries.
//
// linux_dirent64 is laid out as: d_ino[8] d_off[8] d_reclen[2] d_type[1] d_name[...].
// encoding/binary handles alignment — Linux getdents64 buffers guarantee
// little-endian but not struct alignment, so binary.LittleEndian.Uint*
// reads directly from the []byte slice (shift-and-OR) with no alignment
// requirement on the source.
func parseDirents(buf []byte) []proto.Dirent {
	var dirents []proto.Dirent

	for len(buf) > 0 {
		// Minimum fixed-header size: d_ino[8] + d_off[8] + d_reclen[2] + d_type[1] = 19.
		if len(buf) < 19 {
			break
		}

		ino := binary.LittleEndian.Uint64(buf[0:8])
		_ = binary.LittleEndian.Uint64(buf[8:16]) // d_off (unused; advance past)
		reclen := binary.LittleEndian.Uint16(buf[16:18])
		dtype := buf[18]

		if int(reclen) > len(buf) || reclen < 19 {
			break
		}

		// Name is null-terminated starting at offset 19.
		nameBytes := buf[19:reclen]
		before, _, ok := bytes.Cut(nameBytes, []byte{0})
		var name string
		if ok {
			name = string(before)
		} else {
			name = string(nameBytes)
		}

		// Skip . and ..
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

// dtypeToQIDType maps a d_type to proto.QIDType.
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

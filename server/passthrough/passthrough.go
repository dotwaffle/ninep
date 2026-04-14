package passthrough

import (
	"context"
	"fmt"

	"golang.org/x/sys/unix"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

// Node represents a file or directory in the passthrough filesystem. It holds
// an OS file descriptor and delegates all operations to the host OS via *at
// syscalls. For directories, the fd is opened with O_RDONLY|O_DIRECTORY. For
// other files, the fd is opened with O_PATH (reopened via /proc/self/fd/N
// for actual I/O).
//
// parentFd and name are stored for symlink nodes so Readlink can use
// Readlinkat(parentFd, name) to read the symlink target.
type Node struct {
	server.Inode
	fd       int
	root     *Root
	parentFd int    // parent directory fd, for readlinkat
	name     string // entry name in parent, for readlinkat
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

// NewRoot creates a new passthrough filesystem root from the given host
// directory path. The path must refer to an existing directory.
func NewRoot(hostPath string, opts ...Option) (*Root, error) {
	fd, err := unix.Open(hostPath, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, fmt.Errorf("open root %s: %w", hostPath, err)
	}

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("stat root %s: %w", hostPath, err)
	}

	r := &Root{
		Node:     Node{fd: fd},
		hostPath: hostPath,
		mapper:   IdentityMapper(),
	}
	for _, opt := range opts {
		opt(r)
	}

	r.root = r
	r.Init(statToQID(&st), r)

	return r, nil
}

// Compile-time interface assertions for Root.
var (
	_ server.Node          = (*Root)(nil)
	_ server.InodeEmbedder = (*Root)(nil)
	_ server.NodeOpener    = (*Root)(nil)
	_ server.NodeGetattrer = (*Root)(nil)
	_ server.NodeSetattrer = (*Root)(nil)
	_ server.NodeCloser    = (*Root)(nil)
	_ server.NodeStatFSer  = (*Root)(nil)
)

// Compile-time interface assertions for Node.
var (
	_ server.Node          = (*Node)(nil)
	_ server.InodeEmbedder = (*Node)(nil)
	_ server.NodeOpener    = (*Node)(nil)
	_ server.NodeGetattrer = (*Node)(nil)
	_ server.NodeSetattrer = (*Node)(nil)
	_ server.NodeCloser    = (*Node)(nil)
	_ server.NodeStatFSer  = (*Node)(nil)
)

// Open opens the node. For directories, returns nil (readdir uses the
// NodeReaddirer interface). For files, reopens the O_PATH fd via
// /proc/self/fd/N with the requested flags.
func (n *Node) Open(_ context.Context, flags uint32) (server.FileHandle, uint32, error) {
	if n.QID().Type == proto.QTDIR {
		return nil, 0, nil
	}
	// Reopen via /proc/self/fd/N to convert O_PATH fd to a usable fd.
	path := fmt.Sprintf("/proc/self/fd/%d", n.fd)
	fd, err := unix.Open(path, int(flags)&^unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, 0, toProtoErr(err)
	}
	return &fileHandle{fd: fd}, 0, nil
}

// Getattr returns file attributes from fstat on the node's fd.
func (n *Node) Getattr(_ context.Context, _ proto.AttrMask) (proto.Attr, error) {
	var st unix.Stat_t
	if err := unix.Fstat(n.fd, &st); err != nil {
		return proto.Attr{}, toProtoErr(err)
	}
	return statToAttr(&st, n.root.mapper), nil
}

// Setattr modifies file attributes based on the valid mask.
func (n *Node) Setattr(_ context.Context, attr proto.SetAttr) error {
	if attr.Valid&proto.SetAttrMode != 0 {
		if err := unix.Fchmod(n.fd, attr.Mode); err != nil {
			return toProtoErr(err)
		}
	}
	if attr.Valid&proto.SetAttrUID != 0 || attr.Valid&proto.SetAttrGID != 0 {
		uid := -1
		gid := -1
		if attr.Valid&proto.SetAttrUID != 0 {
			hostUID, _ := n.root.mapper.ToHost(attr.UID, 0)
			uid = int(hostUID)
		}
		if attr.Valid&proto.SetAttrGID != 0 {
			_, hostGID := n.root.mapper.ToHost(0, attr.GID)
			gid = int(hostGID)
		}
		path := fmt.Sprintf("/proc/self/fd/%d", n.fd)
		if err := unix.Lchown(path, uid, gid); err != nil {
			return toProtoErr(err)
		}
	}
	if attr.Valid&proto.SetAttrSize != 0 {
		if err := unix.Ftruncate(n.fd, int64(attr.Size)); err != nil {
			return toProtoErr(err)
		}
	}
	if attr.Valid&proto.SetAttrATime != 0 || attr.Valid&proto.SetAttrMTime != 0 {
		ts := []unix.Timespec{
			{Sec: 0, Nsec: unix.UTIME_OMIT},
			{Sec: 0, Nsec: unix.UTIME_OMIT},
		}
		if attr.Valid&proto.SetAttrATime != 0 {
			ts[0] = unix.Timespec{Sec: int64(attr.ATimeSec), Nsec: int64(attr.ATimeNSec)}
		}
		if attr.Valid&proto.SetAttrMTime != 0 {
			ts[1] = unix.Timespec{Sec: int64(attr.MTimeSec), Nsec: int64(attr.MTimeNSec)}
		}
		path := fmt.Sprintf("/proc/self/fd/%d", n.fd)
		if err := unix.UtimesNanoAt(unix.AT_FDCWD, path, ts, 0); err != nil {
			return toProtoErr(err)
		}
	}
	return nil
}

// StatFS returns filesystem statistics for the filesystem containing this node.
func (n *Node) StatFS(_ context.Context) (proto.FSStat, error) {
	var st unix.Statfs_t
	if err := unix.Fstatfs(n.fd, &st); err != nil {
		return proto.FSStat{}, toProtoErr(err)
	}
	return proto.FSStat{
		Type:    uint32(st.Type),
		BSize:   uint32(st.Bsize),
		Blocks:  st.Blocks,
		BFree:   st.Bfree,
		BAvail:  st.Bavail,
		Files:   st.Files,
		FFree:   st.Ffree,
		FSID:    uint64(uint32(st.Fsid.Val[0])) | uint64(uint32(st.Fsid.Val[1]))<<32,
		NameLen: uint32(st.Namelen),
	}, nil
}

// Close releases the OS file descriptor held by this node.
func (n *Node) Close(_ context.Context) error {
	return toProtoErr(unix.Close(n.fd))
}

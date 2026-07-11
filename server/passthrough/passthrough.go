//go:build linux || freebsd

package passthrough

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

// NewRoot creates a new passthrough filesystem root from the given host
// directory path. The path must refer to an existing directory.
func NewRoot(hostPath string, opts ...Option) (*Root, error) {
	fd, err := unix.Open(hostPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open root %s: %w", hostPath, err)
	}

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("stat root %s: %w", hostPath, err)
	}

	r := &Root{
		Node:   Node{fd: fd},
		mapper: IdentityMapper(),
		dev:    uint64(st.Dev),
		ino:    st.Ino,
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.mapper.ToHost == nil || r.mapper.FromHost == nil {
		_ = unix.Close(fd)
		return nil, errors.New("passthrough: UID mapper callbacks must not be nil")
	}

	r.root = r
	r.Init(statToQID(&st), r)

	return r, nil
}

// deviceNodeDenied reports whether mode requests a block or character device
// node that this filesystem refuses to create. Device nodes are denied unless
// WithDeviceNodes was set; FIFOs, sockets, and regular files are unaffected.
func (n *Node) deviceNodeDenied(mode proto.FileMode) bool {
	if n.root.allowDevice {
		return false
	}
	switch uint32(mode) & unix.S_IFMT {
	case unix.S_IFBLK, unix.S_IFCHR:
		return true
	default:
		return false
	}
}

// lookupParent resolves a ".." walk element. It clamps at the export root: if
// this node's directory is the export root (matching the recorded dev/ino),
// ".." resolves to the root directory itself instead of the host parent, so a
// peer cannot walk above the exported tree. A fresh fd is always opened (never
// shared with the root node) so a later clunk releases only that fd.
//
// For a non-root node the host parent is always at or below the export root,
// because every node is reached by descending from the root through
// single-component O_NOFOLLOW walks; no symlink or multi-component element can
// jump outside the tree. The next ".." that reaches the root then clamps.
func (n *Node) lookupParent() (server.Node, error) {
	var st unix.Stat_t
	if err := unix.Fstat(n.fd, &st); err != nil {
		return nil, toProtoErr(err)
	}
	target := ".."
	clampToRoot := uint64(st.Dev) == n.root.dev && st.Ino == n.root.ino
	if clampToRoot {
		target = "."
	}
	fd, err := unix.Openat(n.fd, target, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, toProtoErr(err)
	}
	var cst unix.Stat_t
	if err := unix.Fstat(fd, &cst); err != nil {
		_ = unix.Close(fd)
		return nil, toProtoErr(err)
	}
	var child *Node
	if clampToRoot {
		// The clamped child IS the export root. Leave the parent anchor
		// empty so parent-anchored *at operations (Setattr chown/utimes,
		// Readlink, FreeBSD reopen) treat it as a root node and act on the
		// held fd or the root's host path, never resolving rootFd/".." up
		// into the host parent outside the export. Anchoring the clamped
		// child would point these ops at the host parent.
		child = &Node{fd: fd, root: n.root, dev: uint64(cst.Dev)}
	} else {
		var cerr error
		child, cerr = n.childNode(fd, "..", uint64(cst.Dev))
		if cerr != nil {
			return nil, toProtoErr(cerr)
		}
	}
	child.Init(statToQID(&cst), child)
	child.EmbeddedInode().SetPrunable()
	return child, nil
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

// Open opens the node. Directories receive a cursor-bearing raw readdir
// handle; other nodes receive an offset-based data handle.
func (n *Node) Open(_ context.Context, flags uint32) (server.FileHandle, uint32, error) {
	if n.QID().Type == proto.QTDIR {
		fd, err := n.openResolved(unix.O_RDONLY | unix.O_DIRECTORY)
		if err != nil {
			return nil, 0, toProtoErr(err)
		}
		return &dirHandle{fd: fd}, 0, nil
	}
	fd, err := n.openResolved(flags)
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
		if err := n.chmodResolved(attr.Mode); err != nil {
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
		if err := n.chownResolved(uid, gid); err != nil {
			return toProtoErr(err)
		}
	}
	if attr.Valid&proto.SetAttrSize != 0 {
		if err := n.truncateResolved(attr.Size); err != nil {
			return toProtoErr(err)
		}
	}
	if attr.Valid&proto.SetAttrATime != 0 || attr.Valid&proto.SetAttrMTime != 0 {
		// UTIME_OMIT is encoded as Nsec only; use a Timespec literal for the
		// sentinel and unix.NsecToTimespec for real timestamps so the field
		// types resolve correctly across 32- and 64-bit Timespec layouts.
		omit := unix.NsecToTimespec(0)
		omit.Nsec = unix.UTIME_OMIT
		ts := []unix.Timespec{omit, omit}
		if attr.Valid&proto.SetAttrATime != 0 {
			ts[0] = unix.NsecToTimespec(int64(attr.ATimeSec)*1e9 + int64(attr.ATimeNSec))
		}
		if attr.Valid&proto.SetAttrMTime != 0 {
			ts[1] = unix.NsecToTimespec(int64(attr.MTimeSec)*1e9 + int64(attr.MTimeNSec))
		}
		if err := n.setTimesResolved(ts); err != nil {
			return toProtoErr(err)
		}
	}
	return nil
}

// Close releases the OS file descriptors held by this node: its own fd and
// the duplicated parent-directory anchor (see childNode).
func (n *Node) Close(_ context.Context) error {
	err := unix.Close(n.fd)
	if n.parentFd > 0 {
		_ = unix.Close(n.parentFd)
	}
	return toProtoErr(err)
}

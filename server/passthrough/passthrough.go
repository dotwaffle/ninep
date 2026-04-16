//go:build linux || freebsd

package passthrough

import (
	"context"
	"fmt"

	"golang.org/x/sys/unix"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

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
// NodeReaddirer interface). For non-directories, opens a fresh fd via the
// parent directory's fd (Openat(parentFd, name, flags, 0)) -- replaces the
// Linux /proc/self/fd reopen so the same code path works on FreeBSD.
//
// Root nodes (parentFd == 0, empty name) re-open via the host path stored
// on Root.
func (n *Node) Open(_ context.Context, flags uint32) (server.FileHandle, uint32, error) {
	if n.QID().Type == proto.QTDIR {
		return nil, 0, nil
	}
	if n.parentFd == 0 && n.name == "" {
		// Root or root-equivalent: re-open via stored host path.
		if n.root == nil || n.root.hostPath == "" {
			return nil, 0, proto.EINVAL
		}
		fd, err := unix.Open(n.root.hostPath, int(flags)&^unix.O_NOFOLLOW, 0)
		if err != nil {
			return nil, 0, toProtoErr(err)
		}
		return &fileHandle{fd: fd}, 0, nil
	}
	fd, err := unix.Openat(n.parentFd, n.name, int(flags)&^unix.O_NOFOLLOW, 0)
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
//
// Lchown and UtimesNanoAt use parent-anchored *at calls (no /proc/self/fd):
// /proc is not mounted on FreeBSD by default. For root nodes (no parentFd),
// fall back to operations on the held fd or the stored host path.
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
		if n.parentFd == 0 && n.name == "" {
			// Root: fchown directly on the held fd.
			if err := unix.Fchown(n.fd, uid, gid); err != nil {
				return toProtoErr(err)
			}
		} else {
			if err := unix.Fchownat(n.parentFd, n.name, uid, gid, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return toProtoErr(err)
			}
		}
	}
	if attr.Valid&proto.SetAttrSize != 0 {
		if err := unix.Ftruncate(n.fd, int64(attr.Size)); err != nil {
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
		if n.parentFd == 0 && n.name == "" {
			// Root: utimes via stored host path.
			if n.root == nil || n.root.hostPath == "" {
				return proto.EINVAL
			}
			if err := unix.UtimesNanoAt(unix.AT_FDCWD, n.root.hostPath, ts, 0); err != nil {
				return toProtoErr(err)
			}
		} else {
			if err := unix.UtimesNanoAt(n.parentFd, n.name, ts, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return toProtoErr(err)
			}
		}
	}
	return nil
}

// Close releases the OS file descriptor held by this node.
func (n *Node) Close(_ context.Context) error {
	return toProtoErr(unix.Close(n.fd))
}

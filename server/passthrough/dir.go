//go:build linux || freebsd

package passthrough

import (
	"context"

	"golang.org/x/sys/unix"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

// Link creates a hard link named name in this directory pointing to target.
//
// Uses parent-anchored Linkat (srcParentFd, srcName, dstDirFd, dstName, 0)
// instead of the legacy /proc/self/fd Linkat trick: /proc isn't mounted on
// FreeBSD by default, and AT_EMPTY_PATH is unavailable. The held parentFd
// supplies the same TOCTOU-resistant inode anchoring on Linux without the
// /proc dependency.
func (n *Node) Link(_ context.Context, target server.Node, name string) error {
	if n.QID().Type != proto.QTDIR {
		return proto.ENOTDIR
	}

	var srcParentFd int
	var srcName string
	switch t := target.(type) {
	case *Node:
		srcParentFd = t.parentFd
		srcName = t.name
	case *Root:
		srcParentFd = t.parentFd
		srcName = t.name
	default:
		return proto.EINVAL
	}
	if srcParentFd == 0 || srcName == "" {
		return proto.EINVAL
	}

	if err := unix.Linkat(srcParentFd, srcName, n.fd, name, 0); err != nil {
		return toProtoErr(err)
	}
	return nil
}

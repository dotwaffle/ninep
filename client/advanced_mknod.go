package client

import (
	"context"
	"errors"

	"github.com/dotwaffle/ninep/proto"
)

// Mknod creates a device/fifo/special-file node at path. Mode carries
// the POSIX mode bits -- the high-order bits select the node type
// (S_IFIFO/S_IFCHR/S_IFBLK/S_IFSOCK etc.), the low-order bits are the
// permission bits. major/minor identify the device; gid sets the
// owning group.
//
// The returned [*File] is a stat-only handle bound to the new node --
// 9P has no "open a device node" mechanism at this layer; the fid is
// useful for [File.Close] (release the fid) and capability-level
// operations that do not require Tlopen/Topen.
//
// Requires a 9P2000.L-negotiated Conn; returns wrapped [ErrNotSupported]
// on .u (Tmknod is .L-only; .u callers historically encoded device
// nodes via the Tcreate extension field).
//
// path must be non-root. The parent directories along path must
// already exist (Mknod does not recursively create intermediates); a
// missing parent surfaces the server's ENOENT as a *[Error].
//
// Fid lifecycle: acquires up to two fids (parent dir + newly-created
// node). Both are clunked and released on every exit path -- the parent
// dirFid at method exit, the newFid only on post-Tmknod walk failure.
// On success the newFid lives on as the returned *File.fid until
// [File.Close].
func (c *Conn) Mknod(ctx context.Context, path string, mode proto.FileMode, major, minor, gid uint32) (*File, error) {
	if err := c.requireDialect(protocolL, "Mknod"); err != nil {
		return nil, err
	}
	root := c.Root()
	if root == nil {
		return nil, errors.New("client: Mknod requires a prior Attach")
	}
	if err := root.beginOp(); err != nil {
		return nil, err
	}
	defer root.endOp()
	full := splitPath(path)
	if len(full) == 0 {
		return nil, errors.New("client: Mknod requires a non-root path")
	}
	parents, name := full[:len(full)-1], full[len(full)-1]

	// Walk to the parent directory (zero-step walk for "/" clones the
	// root fid).
	dirFid, dirCleanup, err := c.walkNew(ctx, root.fid, parents, "parent")
	if err != nil {
		return nil, err
	}

	// Issue Tmknod. mode is passed through as a uint32 -- the wire layer
	// accepts any FileMode value; server-side interpretation determines
	// node type (FIFO/char-dev/block-dev/socket) from the S_IF* bits.
	qid, err := c.Raw().Tmknod(ctx, dirFid, name, uint32(mode), major, minor, gid)
	if err != nil {
		dirCleanup()
		return nil, err
	}

	// Walk from dirFid to the new node via a fresh fid so the caller
	// gets a *File handle. dirFid is clunked before return regardless
	// of the walk outcome; newFid's cleanup is intentionally unused on
	// success -- ownership moves into the returned *File.
	newFid, _, walkErr := c.walkNew(ctx, dirFid, []string{name}, "new node")
	dirCleanup()
	if walkErr != nil {
		return nil, walkErr
	}
	// iounit=0: the node is stat-only; no negotiated chunk size.
	return newFile(c, newFid, qid, 0), nil
}

package client

import (
	"context"
	"fmt"

	"github.com/dotwaffle/ninep/proto"
)

// Mknod creates a device/fifo/special-file node named name under the
// directory at parentPath. Mode carries the POSIX mode bits — the
// high-order bits select the node type (S_IFIFO/S_IFCHR/S_IFBLK/S_IFSOCK
// etc.), the low-order bits are the permission bits. major/minor
// identify the device; gid sets the owning group.
//
// The returned [*File] is a stat-only handle bound to the new node —
// 9P has no "open a device node" mechanism at this layer; the fid is
// useful for [File.Close] (release the fid) and capability-level
// operations that do not require Tlopen/Topen.
//
// Requires a 9P2000.L-negotiated Conn; returns wrapped [ErrNotSupported]
// on .u (Tmknod is .L-only; .u callers historically encoded device
// nodes via the Tcreate extension field).
//
// parentPath may be "/" (or equivalent: "", "."). name must be a single
// path component; callers cannot create intermediate directories via
// Mknod.
//
// Fid lifecycle: acquires up to two fids (parent dir + newly-created
// node). Both are clunked and released on every exit path — the parent
// dirFid at method exit, the newFid only on post-Tmknod walk failure.
// On success the newFid lives on as the returned *File.fid until
// [File.Close].
func (c *Conn) Mknod(ctx context.Context, parentPath, name string, mode proto.FileMode, major, minor, gid uint32) (*File, error) {
	if err := c.requireDialect(protocolL, "Mknod"); err != nil {
		return nil, err
	}
	root := c.Root()
	if root == nil {
		return nil, fmt.Errorf("client: Mknod requires a prior Attach")
	}
	if name == "" {
		return nil, fmt.Errorf("client: Mknod requires a non-empty name")
	}
	parents := splitPath(parentPath)

	// Walk to the parent directory (zero-step walk for "/" clones the
	// root fid).
	dirFid, err := c.fids.acquire()
	if err != nil {
		return nil, err
	}
	qids, err := c.Walk(ctx, root.fid, dirFid, parents)
	if err != nil {
		c.fids.release(dirFid)
		return nil, err
	}
	if len(parents) > 0 && len(qids) != len(parents) {
		c.fids.release(dirFid)
		return nil, fmt.Errorf("client: partial walk to parent (%d of %d steps)", len(qids), len(parents))
	}

	// Issue Tmknod. mode is passed through as a uint32 — the wire layer
	// accepts any FileMode value; server-side interpretation determines
	// node type (FIFO/char-dev/block-dev/socket) from the S_IF* bits.
	qid, err := c.Raw().Tmknod(ctx, dirFid, name, uint32(mode), major, minor, gid)
	if err != nil {
		_ = c.Clunk(context.Background(), dirFid)
		c.fids.release(dirFid)
		return nil, err
	}

	// Walk from dirFid to the new node via a fresh fid so the caller
	// gets a *File handle. dirFid is clunked before return regardless
	// of the walk outcome.
	newFid, err := c.fids.acquire()
	if err != nil {
		_ = c.Clunk(context.Background(), dirFid)
		c.fids.release(dirFid)
		return nil, err
	}
	_, walkErr := c.Walk(ctx, dirFid, newFid, []string{name})
	_ = c.Clunk(context.Background(), dirFid)
	c.fids.release(dirFid)
	if walkErr != nil {
		c.fids.release(newFid)
		return nil, walkErr
	}
	// iounit=0: the node is stat-only; no negotiated chunk size.
	return newFile(c, newFid, qid, 0), nil
}

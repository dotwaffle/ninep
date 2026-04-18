package client

import (
	"context"
	"fmt"
)

// Link creates a hard link at newPath pointing at the existing file at
// existingPath. Both entries reference the same underlying inode after
// the call returns — reads and writes on either name see the same data.
//
// Requires a 9P2000.L-negotiated Conn; returns wrapped [ErrNotSupported]
// on .u (9P2000.u has no Tlink wire op).
//
// Both paths must be non-root. The parent directory of newPath must
// exist (Link does not create parents). The server is required to reject
// cross-device / cross-mount links — surfaces as a *[Error] carrying the
// server's errno (typically EXDEV or EPERM).
//
// Fid lifecycle: Link acquires two fids (source file, dest parent dir);
// both are clunked and released on every exit path.
func (c *Conn) Link(ctx context.Context, existingPath, newPath string) error {
	if err := c.requireDialect(protocolL, "Link"); err != nil {
		return err
	}
	root := c.Root()
	if root == nil {
		return fmt.Errorf("client: Link requires a prior Attach")
	}
	srcFull := splitPath(existingPath)
	dstFull := splitPath(newPath)
	if len(srcFull) == 0 || len(dstFull) == 0 {
		return fmt.Errorf("client: Link requires non-root paths")
	}
	dstParents := dstFull[:len(dstFull)-1]
	dstName := dstFull[len(dstFull)-1]

	// Walk to the source file fid.
	srcFid, err := c.fids.acquire()
	if err != nil {
		return err
	}
	qids, err := c.Walk(ctx, root.fid, srcFid, srcFull)
	if err != nil {
		c.fids.release(srcFid)
		return err
	}
	if len(qids) != len(srcFull) {
		c.fids.release(srcFid)
		return fmt.Errorf("client: partial walk to source (%d of %d steps)", len(qids), len(srcFull))
	}

	// Walk to the dest parent dir. On any failure here, srcFid must be
	// clunked + released.
	dstDirFid, err := c.fids.acquire()
	if err != nil {
		_ = c.Clunk(context.Background(), srcFid)
		c.fids.release(srcFid)
		return err
	}
	qids, err = c.Walk(ctx, root.fid, dstDirFid, dstParents)
	if err != nil {
		c.fids.release(dstDirFid)
		_ = c.Clunk(context.Background(), srcFid)
		c.fids.release(srcFid)
		return err
	}
	if len(dstParents) > 0 && len(qids) != len(dstParents) {
		c.fids.release(dstDirFid)
		_ = c.Clunk(context.Background(), srcFid)
		c.fids.release(srcFid)
		return fmt.Errorf("client: partial walk to dest-parent (%d of %d steps)", len(qids), len(dstParents))
	}

	// Tlink wire order is (dfid, fid, name): dfid is the parent dir of
	// the new link, fid is the target being linked.
	linkErr := c.Raw().Tlink(ctx, dstDirFid, srcFid, dstName)
	_ = c.Clunk(context.Background(), dstDirFid)
	c.fids.release(dstDirFid)
	_ = c.Clunk(context.Background(), srcFid)
	c.fids.release(srcFid)
	return linkErr
}

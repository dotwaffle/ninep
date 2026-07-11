package client

import (
	"context"
	"errors"
)

// Link creates a hard link at newPath pointing at the existing file at
// existingPath. Both entries reference the same underlying inode after
// the call returns -- reads and writes on either name see the same data.
//
// Requires a 9P2000.L-negotiated Conn; returns wrapped [ErrNotSupported]
// on .u (9P2000.u has no Tlink wire op).
//
// Both paths must be non-root. The parent directory of newPath must
// exist (Link does not create parents). The server is required to reject
// cross-device / cross-mount links -- surfaces as a *[Error] carrying the
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
		return errors.New("client: Link requires a prior Attach")
	}
	if err := root.beginOp(); err != nil {
		return err
	}
	defer root.endOp()
	srcFull := splitPath(existingPath)
	dstFull := splitPath(newPath)
	if len(srcFull) == 0 || len(dstFull) == 0 {
		return errors.New("client: Link requires non-root paths")
	}
	dstParents := dstFull[:len(dstFull)-1]
	dstName := dstFull[len(dstFull)-1]

	// Walk to the source file fid.
	srcFid, srcCleanup, err := c.walkNew(ctx, root.fid, srcFull, "source")
	if err != nil {
		return err
	}

	// Walk to the dest parent dir. On any failure here, srcFid must be
	// clunked + released.
	dstDirFid, dstCleanup, err := c.walkNew(ctx, root.fid, dstParents, "dest-parent")
	if err != nil {
		srcCleanup()
		return err
	}

	// Tlink wire order is (dfid, fid, name): dfid is the parent dir of
	// the new link, fid is the target being linked.
	linkErr := c.Raw().Tlink(ctx, dstDirFid, srcFid, dstName)
	dstCleanup()
	srcCleanup()
	return linkErr
}

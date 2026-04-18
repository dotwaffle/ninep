package client

import (
	"context"
	"fmt"
)

// Rename moves the entry at fromPath to toPath. On 9P2000.L the wire op
// is Trenameat(oldDirFid, oldName, newDirFid, newName) — both parent
// directories are walked, the rename is issued, and both parent fids
// are clunked regardless of outcome (Pitfall 10).
//
// On 9P2000.u, Rename returns wrapped [ErrNotSupported]. Raw.Trename is
// gated to .L at the raw layer (p9l codec only), and this library's .u
// server does not implement a Trename-via-Twstat fallback. Callers
// needing .u rename semantics should use [Raw] primitives directly and
// speak Twstat with the server's expectations.
//
// Both fromPath and toPath must be non-root. Intermediate directories
// on either path must exist (Rename does not create parents).
//
// Rename-while-open preservation (21-RESEARCH.md Pitfall 5): a *File
// opened against fromPath before the rename remains valid — the fid
// stays bound to the same inode, not the path, and subsequent
// [File.Read] / [File.Write] continue against the renamed node. This
// matches Linux v9fs kernel semantics.
//
// Fid lifecycle: on .L, Rename acquires two fids (source parent, dest
// parent). Every exit path — successful rename, source-walk failure,
// dest-walk failure, Trenameat error — clunks and releases both fids
// (T-21-02-03). No leaks on partial failure.
func (c *Conn) Rename(ctx context.Context, fromPath, toPath string) error {
	if err := c.requireDialect(protocolL, "Rename"); err != nil {
		return err
	}
	root := c.Root()
	if root == nil {
		return fmt.Errorf("client: Rename requires a prior Attach")
	}
	fromFull := splitPath(fromPath)
	toFull := splitPath(toPath)
	if len(fromFull) == 0 || len(toFull) == 0 {
		return fmt.Errorf("client: Rename requires non-root paths")
	}
	fromParents := fromFull[:len(fromFull)-1]
	fromName := fromFull[len(fromFull)-1]
	toParents := toFull[:len(toFull)-1]
	toName := toFull[len(toFull)-1]

	// Walk source parent.
	oldDirFid, err := c.fids.acquire()
	if err != nil {
		return err
	}
	qids, err := c.Walk(ctx, root.fid, oldDirFid, fromParents)
	if err != nil {
		c.fids.release(oldDirFid)
		return err
	}
	if len(fromParents) > 0 && len(qids) != len(fromParents) {
		c.fids.release(oldDirFid)
		return fmt.Errorf("client: partial walk to from-parent (%d of %d steps)", len(qids), len(fromParents))
	}

	// Walk dest parent. On any failure here, the source-parent fid must
	// be clunked + released before returning.
	newDirFid, err := c.fids.acquire()
	if err != nil {
		_ = c.Clunk(context.Background(), oldDirFid)
		c.fids.release(oldDirFid)
		return err
	}
	qids, err = c.Walk(ctx, root.fid, newDirFid, toParents)
	if err != nil {
		c.fids.release(newDirFid)
		_ = c.Clunk(context.Background(), oldDirFid)
		c.fids.release(oldDirFid)
		return err
	}
	if len(toParents) > 0 && len(qids) != len(toParents) {
		c.fids.release(newDirFid)
		_ = c.Clunk(context.Background(), oldDirFid)
		c.fids.release(oldDirFid)
		return fmt.Errorf("client: partial walk to to-parent (%d of %d steps)", len(qids), len(toParents))
	}

	// Issue Trenameat; clunk+release both dir fids regardless of
	// outcome. newDirFid first so its Rclunk is processed before the
	// oldDirFid release in the common case, but either order is safe —
	// the fids are independent server-side.
	renameErr := c.Raw().Trenameat(ctx, oldDirFid, fromName, newDirFid, toName)
	_ = c.Clunk(context.Background(), newDirFid)
	c.fids.release(newDirFid)
	_ = c.Clunk(context.Background(), oldDirFid)
	c.fids.release(oldDirFid)
	return renameErr
}

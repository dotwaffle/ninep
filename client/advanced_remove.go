package client

import (
	"context"
	"fmt"

	"github.com/dotwaffle/ninep/proto"
)

// atRemoveDir is the AT_REMOVEDIR flag bit passed to Tunlinkat to request
// directory removal. Non-zero on a regular file yields ENOTDIR; zero on a
// directory yields EISDIR. See 21-RESEARCH.md Pitfall 9.
const atRemoveDir = 0x200

// Remove removes the file or directory at path. On 9P2000.L the wire op is
// Tunlinkat against the parent fid; auto-detects directories via a probe
// walk that reads QID.Type and sets the AT_REMOVEDIR flag accordingly
// (Pitfall 9). On 9P2000.u, Remove returns wrapped [ErrNotSupported] —
// .u lacks Tunlinkat and this library's server does not implement a
// Tremove handler (so a .u fallback cannot succeed anywhere).
//
// The path must be non-root. All intermediate parent directories must
// exist; a missing parent surfaces the server's ENOENT as a *[Error].
//
// Fid lifecycle: Remove acquires up to two fids (parent + probe target),
// clunks and releases both on every exit path — no fid leaks on any
// failure mode (T-21-02-03). No separate Clunk is needed for the probe
// fid; Tunlinkat operates against the parent fid and the probe exists
// only to determine the AT_REMOVEDIR flag value.
func (c *Conn) Remove(ctx context.Context, p string) error {
	if err := c.requireDialect(protocolL, "Remove"); err != nil {
		return err
	}
	root := c.Root()
	if root == nil {
		return fmt.Errorf("client: Remove requires a prior Attach")
	}
	full := splitPath(p)
	if len(full) == 0 {
		return fmt.Errorf("client: Remove requires a non-root path")
	}
	parents := full[:len(full)-1]
	name := full[len(full)-1]

	// Walk to the parent directory.
	dirFid, err := c.fids.acquire()
	if err != nil {
		return err
	}
	qids, err := c.Walk(ctx, root.fid, dirFid, parents)
	if err != nil {
		c.fids.release(dirFid)
		return err
	}
	if len(parents) > 0 && len(qids) != len(parents) {
		c.fids.release(dirFid)
		return fmt.Errorf("client: partial walk to parent (%d of %d steps)", len(qids), len(parents))
	}

	// Probe walk: clone dirFid into a fresh fid stepped one level to
	// `name`, read the QID type, then clunk+release the probe. This
	// lets Remove pick the right AT_REMOVEDIR flag without the caller
	// supplying it. A failed probe (ENOENT) surfaces immediately.
	probeFid, err := c.fids.acquire()
	if err != nil {
		_ = c.Clunk(context.Background(), dirFid)
		c.fids.release(dirFid)
		return err
	}
	probeQIDs, err := c.Walk(ctx, dirFid, probeFid, []string{name})
	if err != nil {
		c.fids.release(probeFid)
		_ = c.Clunk(context.Background(), dirFid)
		c.fids.release(dirFid)
		return err
	}
	if len(probeQIDs) != 1 {
		// Partial walk on a single-step op means the target doesn't
		// exist. probeFid is NOT server-bound; no Clunk.
		c.fids.release(probeFid)
		_ = c.Clunk(context.Background(), dirFid)
		c.fids.release(dirFid)
		return fmt.Errorf("client: Remove target not found: %s", name)
	}
	flags := uint32(0)
	if probeQIDs[0].Type&proto.QTDIR != 0 {
		flags = atRemoveDir
	}
	_ = c.Clunk(context.Background(), probeFid)
	c.fids.release(probeFid)

	// Issue Tunlinkat; clunk+release the parent fid regardless of
	// outcome.
	unlinkErr := c.Raw().Tunlinkat(ctx, dirFid, name, flags)
	_ = c.Clunk(context.Background(), dirFid)
	c.fids.release(dirFid)
	return unlinkErr
}

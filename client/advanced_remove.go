package client

import (
	"context"
	"errors"

	"github.com/dotwaffle/ninep/proto"
)

// atRemoveDir is the AT_REMOVEDIR flag bit passed to Tunlinkat to request
// directory removal. Non-zero on a regular file yields ENOTDIR; zero on a
// directory yields EISDIR.
const atRemoveDir = 0x200

// Remove removes the file or directory at path. On 9P2000.L the wire op is
// Tunlinkat against the parent fid; directories are auto-detected by
// retry -- flags=0 first, then AT_REMOVEDIR when the server answers
// EISDIR. On 9P2000.u, Remove returns wrapped [ErrNotSupported] - .u
// lacks Tunlinkat and this library's server does not implement a Tremove
// handler (so a .u fallback cannot succeed anywhere).
//
// The path must be non-root. All intermediate parent directories must
// exist; a missing parent surfaces the server's ENOENT as a *[Error].
//
// Fid lifecycle: Remove acquires one fid (the parent directory), clunked
// and released on every exit path - no fid leaks on any failure mode.
func (c *Conn) Remove(ctx context.Context, p string) error {
	if err := c.requireDialect(protocolL, "Remove"); err != nil {
		return err
	}
	root := c.Root()
	if root == nil {
		return errors.New("client: Remove requires a prior Attach")
	}
	full := splitPath(p)
	if len(full) == 0 {
		return errors.New("client: Remove requires a non-root path")
	}
	parents := full[:len(full)-1]
	name := full[len(full)-1]

	// Walk to the parent directory.
	dirFid, dirCleanup, err := c.walkNew(ctx, root.fid, parents, "parent")
	if err != nil {
		return err
	}
	defer dirCleanup()

	// Try the file case first: most removals target files, and the
	// server distinguishes for us -- Tunlinkat with flags=0 on a
	// directory fails with EISDIR, the cue to retry with AT_REMOVEDIR.
	// This replaces the old probe walk (a Twalk+Tclunk round-trip per
	// removal) and closes its TOCTOU window, where the target could
	// change type between the probe and the unlink.
	err = c.Raw().Tunlinkat(ctx, dirFid, name, 0)
	if errors.Is(err, proto.EISDIR) {
		err = c.Raw().Tunlinkat(ctx, dirFid, name, atRemoveDir)
	}
	return err
}

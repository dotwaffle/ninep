package client

import (
	"context"
	"fmt"

	"github.com/dotwaffle/ninep/proto"
)

// Symlink creates a symbolic link at linkPath with the given target string
// and returns a stat-only [*File] handle bound to the new symlink. Per D-01
// (21-CONTEXT.md), symlink creation is path-rooted, so the method lives on
// [*Conn] rather than [*File]. The returned handle is NOT opened — 9P has
// no "open a symlink" op; the fid is useful for [File.Readlink] and
// [File.Close] only.
//
// The linkPath must be non-root. The parent directories along linkPath
// must exist (this method does not recursively create parents); a missing
// parent surfaces the server's ENOENT as a *[Error].
//
// The target string is passed verbatim to the server; this library does
// not interpret it. Callers who later resolve the target via a local
// syscall/filesystem op are responsible for validating its shape (e.g.
// rejecting path components like ".." when a jailed resolution is
// required).
//
// Requires a 9P2000.L-negotiated Conn; returns a wrapped [ErrNotSupported]
// on .u (9P2000.u has no Tsymlink wire op — .u callers must use Tcreate
// with the DMSYMLINK bit set and the target in the create extension field,
// which is out of scope for this high-level surface).
//
// gid is passed as 0 ("server default"). Callers who need explicit gid
// control should use [Raw.Tsymlink] directly.
func (c *Conn) Symlink(ctx context.Context, linkPath, target string) (*File, error) {
	if err := c.requireDialect(protocolL, "Symlink"); err != nil {
		return nil, err
	}
	root := c.Root()
	if root == nil {
		return nil, fmt.Errorf("client: Symlink requires a prior Attach")
	}
	full := splitPath(linkPath)
	if len(full) == 0 {
		return nil, fmt.Errorf("client: Symlink requires a non-root path")
	}
	parents := full[:len(full)-1]
	name := full[len(full)-1]

	// Walk from root to the parent directory of linkPath. A zero-step
	// walk (parents == nil) clones the root fid into dirFid.
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
		// Partial walk — dirFid is NOT server-bound, so no Clunk.
		c.fids.release(dirFid)
		return nil, fmt.Errorf("client: partial walk to parent (%d of %d steps)", len(qids), len(parents))
	}

	// Issue Tsymlink against the parent dirFid. GID=0 defers to the
	// server's default (matches Linux v9fs convention).
	qid, err := c.Raw().Tsymlink(ctx, dirFid, name, target, 0)
	if err != nil {
		_ = c.Clunk(context.Background(), dirFid)
		c.fids.release(dirFid)
		return nil, err
	}

	// Walk from dirFid to the newly-created symlink via a fresh fid so
	// the caller gets a *File handle. dirFid is clunked regardless of
	// the post-Tsymlink walk outcome — the symlink already exists on the
	// server either way.
	symFid, err := c.fids.acquire()
	if err != nil {
		_ = c.Clunk(context.Background(), dirFid)
		c.fids.release(dirFid)
		return nil, err
	}
	_, walkErr := c.Walk(ctx, dirFid, symFid, []string{name})
	_ = c.Clunk(context.Background(), dirFid)
	c.fids.release(dirFid)
	if walkErr != nil {
		c.fids.release(symFid)
		return nil, walkErr
	}
	// iounit=0: the symlink fid is stat-only (never opened), so there is
	// no negotiated chunk size to clamp against.
	_ = proto.QTSYMLINK // compile-time anchor: QID.Type on qid should have this bit set.
	return newFile(c, symFid, qid, 0), nil
}

// Readlink returns the target path of a symbolic link. The file must have
// been opened or walked-to on a symlink node (QID.Type has QTSYMLINK set);
// Readlink on a non-symlink surfaces the server's error (typically EINVAL
// for .L servers) as a *[Error].
//
// Requires a 9P2000.L-negotiated Conn; returns a wrapped [ErrNotSupported]
// on .u (Treadlink is .L-only; .u servers use stat-extension fields to
// carry symlink targets, which is out of scope for this high-level
// surface).
//
// Rename-while-open preservation (21-RESEARCH.md Pitfall 5) applies: if
// the underlying symlink is renamed concurrently, the fid continues to
// point at the same inode and Readlink returns the same target.
func (f *File) Readlink(ctx context.Context) (string, error) {
	if err := f.conn.requireDialect(protocolL, "Readlink"); err != nil {
		return "", err
	}
	return f.conn.Raw().Treadlink(ctx, f.fid)
}

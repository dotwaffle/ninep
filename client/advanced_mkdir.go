package client

import (
	"context"
	"errors"

	"github.com/dotwaffle/ninep/proto"
)

// Mkdir creates the directory at path. mode carries the POSIX
// permission bits; gid sets the owning group.
//
// The returned [*File] is a stat-only handle bound to the new
// directory -- callers wanting to enumerate it open it separately via
// [Conn.OpenDir] on the same path.
//
// Requires a 9P2000.L-negotiated Conn; returns wrapped
// [ErrNotSupported] on .u (Tmkdir is .L-only).
//
// path must be non-root. The parent directories along path must
// already exist (Mkdir does not recursively create intermediates); a
// missing parent surfaces the server's ENOENT as a *[Error].
//
// Fid lifecycle: mirrors [Conn.Mknod] -- acquires up to two fids
// (parent dir + newly-created node), both clunked and released on
// every exit path except the returned *File's fid, which lives on
// until [File.Close].
func (c *Conn) Mkdir(ctx context.Context, path string, mode proto.FileMode, gid uint32) (*File, error) {
	if err := c.requireDialect(protocolL, "Mkdir"); err != nil {
		return nil, err
	}
	root := c.Root()
	if root == nil {
		return nil, errors.New("client: Mkdir requires a prior Attach")
	}
	full := splitPath(path)
	if len(full) == 0 {
		return nil, errors.New("client: Mkdir requires a non-root path")
	}
	parents, name := full[:len(full)-1], full[len(full)-1]

	// Walk to the parent directory (zero-step walk for "/" clones the
	// root fid).
	dirFid, dirCleanup, err := c.walkNew(ctx, root.fid, parents, "parent")
	if err != nil {
		return nil, err
	}

	qid, err := c.Raw().Tmkdir(ctx, dirFid, name, mode, gid)
	if err != nil {
		dirCleanup()
		return nil, err
	}

	// Walk from dirFid to the new directory via a fresh fid so the
	// caller gets a *File handle. dirFid is clunked before return
	// regardless of the walk outcome; newFid's cleanup is intentionally
	// unused on success -- ownership moves into the returned *File.
	newFid, _, walkErr := c.walkNew(ctx, dirFid, []string{name}, "new directory")
	dirCleanup()
	if walkErr != nil {
		return nil, walkErr
	}
	// iounit=0: the node is stat-only; no negotiated chunk size.
	return newFile(c, newFid, qid, 0), nil
}

package client

import (
	"context"
	"errors"
	"fmt"

	"github.com/dotwaffle/ninep/proto"
)

// Mkdir creates a directory named name under the directory at
// parentPath. mode carries the POSIX permission bits; gid sets the
// owning group.
//
// The returned [*File] is a stat-only handle bound to the new
// directory -- callers wanting to enumerate it open it separately via
// [Conn.OpenDir] on the same path.
//
// Requires a 9P2000.L-negotiated Conn; returns wrapped
// [ErrNotSupported] on .u (Tmkdir is .L-only).
//
// parentPath may be "/" (or equivalent: "", "."). name must be a
// single path component; callers cannot create intermediate
// directories via Mkdir.
//
// Fid lifecycle: mirrors [Conn.Mknod] -- acquires up to two fids
// (parent dir + newly-created node), both clunked and released on
// every exit path except the returned *File's fid, which lives on
// until [File.Close].
func (c *Conn) Mkdir(ctx context.Context, parentPath, name string, mode proto.FileMode, gid uint32) (*File, error) {
	if err := c.requireDialect(protocolL, "Mkdir"); err != nil {
		return nil, err
	}
	root := c.Root()
	if root == nil {
		return nil, errors.New("client: Mkdir requires a prior Attach")
	}
	if name == "" {
		return nil, errors.New("client: Mkdir requires a non-empty name")
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

	qid, err := c.Raw().Tmkdir(ctx, dirFid, name, mode, gid)
	if err != nil {
		_ = c.Clunk(context.Background(), dirFid)
		c.fids.release(dirFid)
		return nil, err
	}

	// Walk from dirFid to the new directory via a fresh fid so the
	// caller gets a *File handle. dirFid is clunked before return
	// regardless of the walk outcome.
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

package client

import (
	"context"
	"fmt"

	"github.com/dotwaffle/ninep/proto"
)

// walkNew walks names from base onto a freshly acquired fid and returns
// the fid plus a cleanup func that clunks and releases it. what names the
// walk destination in the partial-walk error ("parent", "source", ...).
//
// On error every acquired resource has already been released and the
// returned cleanup is nil. A partial walk (fewer qids than names) is an
// error here: per Rwalk semantics the new fid is not server-bound in that
// case, so only the number is released, with no Tclunk.
//
// Call cleanup exactly once -- or never, when ownership of the fid moves
// on (into a *File, whose Close takes over the clunk and release).
// cleanup clunks with context.Background(): an undeadlined clunk cannot
// expire mid-flight, so its only failure modes (server Rlerror,
// connection shutdown) leave the fid safe to release either way.
func (c *Conn) walkNew(ctx context.Context, base proto.Fid, names []string, what string) (proto.Fid, func(), error) {
	fid, err := c.fids.acquire()
	if err != nil {
		return 0, nil, err
	}
	qids, err := c.Walk(ctx, base, fid, names)
	if err != nil {
		c.fids.release(fid)
		return 0, nil, err
	}
	if len(names) > 0 && len(qids) != len(names) {
		c.fids.release(fid)
		return 0, nil, fmt.Errorf("client: partial walk to %s (%d of %d steps)", what, len(qids), len(names))
	}
	cleanup := func() {
		_ = c.Clunk(context.Background(), fid)
		c.fids.release(fid)
	}
	return fid, cleanup, nil
}

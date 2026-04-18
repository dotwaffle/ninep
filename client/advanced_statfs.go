package client

import (
	"context"

	"github.com/dotwaffle/ninep/proto"
)

// Statfs queries filesystem-level statistics for the file tree
// containing this [File]'s fid. The return is BY VALUE — a successful
// Statfs always returns a populated [proto.FSStat], so a pointer return
// shape (nil possible) would be misleading (Pitfall 8 in
// 21-RESEARCH.md).
//
// Requires a 9P2000.L-negotiated Conn; returns a wrapped
// [ErrNotSupported] on a .u Conn. The gate fires before any wire op so
// the error message reads "Statfs requires 9P2000.L" rather than
// "Tstatfs requires 9P2000.L".
//
// Statfs does not cache. Each call issues a fresh Tstatfs round-trip.
// Filesystem statistics can change between calls; callers that observe
// them over time should treat each result as a point-in-time snapshot.
func (f *File) Statfs(ctx context.Context) (proto.FSStat, error) {
	if err := f.conn.requireDialect(protocolL, "Statfs"); err != nil {
		return proto.FSStat{}, err
	}
	return f.conn.Raw().Tstatfs(ctx, f.fid)
}

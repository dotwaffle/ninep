package client

import (
	"context"

	"github.com/dotwaffle/ninep/proto"
)

// Setattr mutates metadata fields on the server-side file referenced by
// this [File]. attr.Valid is the bitmask of fields to write — fields
// not named in Valid are ignored by the server. Common patterns:
//
//   - Chmod:    Setattr(ctx, proto.SetAttr{Valid: proto.SetAttrMode, Mode: 0o644})
//   - Chown:    Setattr(ctx, proto.SetAttr{Valid: proto.SetAttrUID|proto.SetAttrGID, UID: u, GID: g})
//   - Truncate: Setattr(ctx, proto.SetAttr{Valid: proto.SetAttrSize, Size: n})
//   - Utime:    Setattr(ctx, proto.SetAttr{Valid: proto.SetAttrATime|proto.SetAttrMTime, ATimeSec: ..., MTimeSec: ...})
//
// Phase 21 ships Setattr only; Chmod/Chown/Truncate wrappers are
// deferred per 21-CONTEXT.md (Claude's Discretion) until consumer
// demand surfaces.
//
// Requires a 9P2000.L-negotiated Conn; returns a wrapped
// [ErrNotSupported] on a .u Conn. The gate fires before any wire op so
// the error message reads "Setattr requires 9P2000.L" rather than
// "Tsetattr requires 9P2000.L".
func (f *File) Setattr(ctx context.Context, attr proto.SetAttr) error {
	if err := f.conn.requireDialect(protocolL, "Setattr"); err != nil {
		return err
	}
	return f.conn.Raw().Tsetattr(ctx, f.fid, attr)
}

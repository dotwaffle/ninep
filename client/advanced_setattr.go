package client

import (
	"context"
	"os"

	"github.com/dotwaffle/ninep/proto"
)

// Setattr mutates metadata fields on the server-side file referenced by
// this [File]. attr.Valid is the bitmask of fields to write — fields
// not named in Valid are ignored by the server.
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

// Chmod changes the [File]'s permissions to mode.
//
// Requires 9P2000.L; returns a wrapped [ErrNotSupported] on a .u Conn.
func (f *File) Chmod(ctx context.Context, mode os.FileMode) error {
	attr := proto.SetAttr{
		Valid: proto.SetAttrMode,
		Mode:  uint32(mode & os.ModePerm),
	}
	return f.Setattr(ctx, attr)
}

// Chown changes the [File]'s owner and group to uid and gid.
//
// Requires 9P2000.L; returns a wrapped [ErrNotSupported] on a .u Conn.
func (f *File) Chown(ctx context.Context, uid, gid uint32) error {
	attr := proto.SetAttr{
		Valid: proto.SetAttrUID | proto.SetAttrGID,
		UID:   uid,
		GID:   gid,
	}
	return f.Setattr(ctx, attr)
}

// Truncate changes the [File]'s size to size.
//
// Requires 9P2000.L; returns a wrapped [ErrNotSupported] on a .u Conn.
func (f *File) Truncate(ctx context.Context, size uint64) error {
	attr := proto.SetAttr{
		Valid: proto.SetAttrSize,
		Size:  size,
	}
	return f.Setattr(ctx, attr)
}

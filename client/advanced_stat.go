package client

import (
	"context"
	"fmt"
	"time"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9u"
)

// FileInfo is the dialect-neutral file metadata snapshot returned by
// [File.Stat]. It carries only the fields both 9P2000.L (Tgetattr) and
// 9P2000.u (Tstat) populate, so callers can stat files without caring
// which dialect the Conn negotiated.
//
// Callers needing dialect-specific fidelity bypass this type:
// [File.Getattr] returns the full .L [proto.Attr] (NLink, Blocks,
// BTime, Gen, DataVersion), and [Raw.Tstat] returns the full .u
// [p9u.Stat] (string UID/GID/MUID, Extension, Type, Dev).
type FileInfo struct {
	// QID identifies the file on the server (type, version, path).
	QID proto.QID
	// Mode carries the permission bits plus the dialect's type bits,
	// passed through from the wire without translation.
	Mode proto.FileMode
	// Size is the file length in bytes.
	Size uint64
	// Atime and Mtime are the last access and modification times.
	// 9P2000.u carries whole seconds on the wire; 9P2000.L supplies
	// nanosecond precision.
	Atime time.Time
	Mtime time.Time
	// UID and GID are the numeric owner and group IDs: Attr.UID/GID on
	// .L, Stat.NUid/NGid on .u. The .u string forms are available via
	// [Raw.Tstat].
	UID uint32
	GID uint32
	// Name is the leaf name as reported by the server. 9P2000.L's
	// Tgetattr has no name field, so Name is empty on .L connections;
	// callers there already hold the path they walked.
	Name string
}

// attrToFileInfo converts a 9P2000.L [proto.Attr] into the neutral
// [FileInfo] shape for [File.Stat]. Fields present only in .L (NLink,
// Blocks, BTime, Gen, DataVersion) are discarded; callers needing them
// invoke [File.Getattr] on a .L Conn directly.
func attrToFileInfo(a proto.Attr) FileInfo {
	return FileInfo{
		QID:   a.QID,
		Mode:  proto.FileMode(a.Mode),
		Size:  a.Size,
		Atime: time.Unix(int64(a.ATimeSec), int64(a.ATimeNSec)),
		Mtime: time.Unix(int64(a.MTimeSec), int64(a.MTimeNSec)),
		UID:   a.UID,
		GID:   a.GID,
	}
}

// statToFileInfo converts a 9P2000.u [p9u.Stat] into the neutral
// [FileInfo] shape for [File.Stat]. The string-typed UID/GID/MUID and
// the Extension, Type, and Dev fields are discarded; callers needing
// them invoke [Raw.Tstat] directly.
func statToFileInfo(st p9u.Stat) FileInfo {
	return FileInfo{
		QID:   st.QID,
		Mode:  st.Mode,
		Size:  st.Length,
		Atime: time.Unix(int64(st.Atime), 0),
		Mtime: time.Unix(int64(st.Mtime), 0),
		UID:   st.NUid,
		GID:   st.NGid,
		Name:  st.Name,
	}
}

// Stat returns a dialect-neutral snapshot of the File's metadata. On
// 9P2000.L connections, Stat issues Tgetattr(fid, AttrBasic); on
// 9P2000.u connections, Stat issues Tstat. Either result is converted
// to the shared [FileInfo] shape.
//
// Fields only one dialect can supply are dropped in the conversion;
// callers that need them use the fidelity paths instead --
// [File.Getattr] for .L's richer [proto.Attr], [Raw.Tstat] for .u's
// full [p9u.Stat] (string UID/GID, Extension).
//
// Stat does NOT mutate f.cachedSize -- that side effect lives in
// [File.Sync] so [File.Seek] with [io.SeekEnd] has a predictable
// refresh primitive.
func (f *File) Stat(ctx context.Context) (FileInfo, error) {
	r := f.conn.Raw()
	switch f.conn.dialect {
	case protocolL:
		attr, err := r.Tgetattr(ctx, f.fid, proto.AttrBasic)
		if err != nil {
			return FileInfo{}, err
		}
		return attrToFileInfo(attr), nil
	case protocolU:
		st, err := r.Tstat(ctx, f.fid)
		if err != nil {
			return FileInfo{}, err
		}
		return statToFileInfo(st), nil
	default:
		return FileInfo{}, fmt.Errorf("%w: %v", ErrDialectInvariant, f.conn.dialect)
	}
}

// Getattr issues Tgetattr(fid, mask) and returns the full 9P2000.L
// [proto.Attr] struct. Exposed for callers that need fields the
// [FileInfo] conversion discards: NLink, Blocks, BTime, Gen,
// DataVersion.
//
// Common masks: [proto.AttrBasic] (mode through blocks -- the
// recommended default), [proto.AttrAll] (every defined attribute), or a
// narrower bitmask when the caller only needs one field
// (e.g. AttrSize for a size refresh).
//
// Requires a 9P2000.L-negotiated Conn; returns a wrapped
// [ErrNotSupported] on a .u Conn. The gate fires before any wire op.
func (f *File) Getattr(ctx context.Context, mask proto.AttrMask) (proto.Attr, error) {
	if err := f.conn.requireDialect(protocolL, "Getattr"); err != nil {
		return proto.Attr{}, err
	}
	return f.conn.Raw().Tgetattr(ctx, f.fid, mask)
}

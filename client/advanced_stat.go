package client

import (
	"context"
	"fmt"
	"strconv"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9u"
)

// attrToStat converts a 9P2000.L [proto.Attr] into a 9P2000.u-compatible
// [p9u.Stat] for [File.Stat]'s dialect-neutral return shape (Pitfall 4
// Option A in 21-RESEARCH.md). Fields present only in .L — NLink, Blocks,
// BTime, Gen, DataVersion — are discarded; callers needing them invoke
// [File.Getattr] on a .L Conn directly.
//
// UID and GID are stored as strings in 9P2000.u; on the .L side they
// are numeric uint32. attrToStat stringifies them as decimal, matching
// the 9P2000.u convention used by most .u servers (per
// ericvh.github.io/9P2000.u). The MUID field (9P2000.u's "last-modifier
// user ID" string) has no .L counterpart and is left empty. Extension
// is left empty for the same reason.
//
// Size (the 2-byte wire stat-length prefix) is zero — the encoder
// rewrites it from EncodedSize() at emit time. Similarly Type/Dev have
// no .L source and map to zero.
//
// This is a pure helper — no I/O, no allocations beyond the two UID/GID
// string conversions. Safe to call on the hot path.
func attrToStat(a proto.Attr) p9u.Stat {
	return p9u.Stat{
		Size:      0,
		Type:      0,
		Dev:       0,
		QID:       a.QID,
		Mode:      proto.FileMode(a.Mode),
		Atime:     uint32(a.ATimeSec),
		Mtime:     uint32(a.MTimeSec),
		Length:    a.Size,
		Name:      "",
		UID:       strconv.FormatUint(uint64(a.UID), 10),
		GID:       strconv.FormatUint(uint64(a.GID), 10),
		MUID:      "",
		Extension: "",
		NUid:      a.UID,
		NGid:      a.GID,
		NMuid:     0,
	}
}

// Stat returns a dialect-neutral snapshot of the File's metadata. On
// 9P2000.L connections, Stat issues Tgetattr(fid, AttrBasic) and
// converts the result via [attrToStat]. On 9P2000.u connections, Stat
// issues Tstat and returns the resulting stat directly.
//
// The return type is [p9u.Stat] on both dialects — a dialect-neutral
// shape per D-16/D-18 in 21-CONTEXT.md (Pitfall 4 Option A). Fields
// present only in .L's richer [proto.Attr] (NLink, Blocks, BTime,
// Gen, DataVersion) are discarded; callers that need them call
// [File.Getattr] directly on a .L Conn.
//
// Stat does NOT mutate f.cachedSize — that side effect lives in
// [File.Sync] so [File.Seek] with [io.SeekEnd] has a predictable
// refresh primitive.
//
// UID and GID on .L are numeric and are stringified as decimal in the
// returned [p9u.Stat]; callers parsing those strings should use
// [strconv.ParseUint]. The numeric values also remain accessible via
// Stat.NUid and Stat.NGid.
func (f *File) Stat(ctx context.Context) (p9u.Stat, error) {
	r := f.conn.Raw()
	switch f.conn.dialect {
	case protocolL:
		attr, err := r.Tgetattr(ctx, f.fid, proto.AttrBasic)
		if err != nil {
			return p9u.Stat{}, err
		}
		return attrToStat(attr), nil
	case protocolU:
		return r.Tstat(ctx, f.fid)
	default:
		return p9u.Stat{}, fmt.Errorf("%w: %v", ErrDialectInvariant, f.conn.dialect)
	}
}

// Getattr issues Tgetattr(fid, mask) and returns the full 9P2000.L
// [proto.Attr] struct. Exposed for callers that need fields attrToStat
// discards: NLink, Blocks, BTime, Gen, DataVersion.
//
// Common masks: [proto.AttrBasic] (mode through blocks — the
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

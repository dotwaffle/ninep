package client

import (
	"context"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
	"github.com/dotwaffle/ninep/proto/p9u"
)

// Advanced-op wire-level wrappers. Each method mirrors the
// [Conn.Clunk] template from ops.go:
//
//  1. requireDialect(protocolL/U, "T<op>") where dialect-gated.
//  2. Build the T-body struct and dispatch via rtrip, which round-trips,
//     translates Rlerror/Rerror into a *Error (releasing the pooled
//     R-message on that path), and type-asserts the expected concrete
//     R struct - releasing the pooled R and surfacing a descriptive
//     error on mismatch.
//  3. Copy out the wire fields the caller needs; putCachedRMsg the
//     pooled struct exactly once on the happy path.
//
// Higher-level *File and *Conn surface methods compose on top of
// these - every wire-level primitive lives here.

// Tsymlink creates a symbolic link named name with the given target in the
// directory referenced by dfid. Returns the new symlink's QID. Requires a
// 9P2000.L-negotiated Conn; returns a wrapped [ErrNotSupported] on .u.
//
// Higher-level path-rooted sugar lives at [Conn.Symlink].
func (r *Raw) Tsymlink(ctx context.Context, dfid proto.Fid, name, target string, gid uint32) (proto.QID, error) {
	if err := r.c.requireDialect(protocolL, "Tsymlink"); err != nil {
		return proto.QID{}, err
	}
	req := &p9l.Tsymlink{DirFid: dfid, Name: name, Target: target, GID: gid}
	rr, err := rtrip[*p9l.Rsymlink](ctx, r.c, req)
	if err != nil {
		return proto.QID{}, err
	}
	qid := rr.QID
	putCachedRMsg(rr)
	return qid, nil
}

// Treadlink returns the target path of the symbolic link referenced by fid.
// Requires a 9P2000.L-negotiated Conn; returns a wrapped [ErrNotSupported]
// on .u.
//
// Higher-level [File.Readlink] sugar wraps this primitive.
func (r *Raw) Treadlink(ctx context.Context, fid proto.Fid) (string, error) {
	if err := r.c.requireDialect(protocolL, "Treadlink"); err != nil {
		return "", err
	}
	rr, err := rtrip[*p9l.Rreadlink](ctx, r.c, &p9l.Treadlink{Fid: fid})
	if err != nil {
		return "", err
	}
	target := rr.Target
	putCachedRMsg(rr)
	return target, nil
}

// Txattrwalk opens an xattr-read fid (newFid) bound to fid + name, returning
// the server-declared size of the attribute value. Size is returned verbatim
// from Rxattrwalk - callers that allocate a buffer MUST bound size against a
// safe ceiling (high-level [File.XattrGet] clamps against
// [proto.MaxDataSize]).
//
// Requires a 9P2000.L-negotiated Conn.
func (r *Raw) Txattrwalk(ctx context.Context, fid, newFid proto.Fid, name string) (uint64, error) {
	if err := r.c.requireDialect(protocolL, "Txattrwalk"); err != nil {
		return 0, err
	}
	req := &p9l.Txattrwalk{Fid: fid, NewFid: newFid, Name: name}
	rr, err := rtrip[*p9l.Rxattrwalk](ctx, r.c, req)
	if err != nil {
		return 0, err
	}
	size := rr.Size
	putCachedRMsg(rr)
	return size, nil
}

// Txattrcreate prepares fid for an xattr-write sequence: the server mutates
// fid to the xattr-write state, and the caller MUST follow with [Raw.Write]
// calls that append the attribute value (total bytes = attrSize), then a
// final [Raw.Clunk] to commit.
//
// The caller's fid is invalidated by the final Clunk - DO NOT reuse the
// same fid value afterwards (pair the Clunk with [Raw.ReleaseFid]).
//
// Requires a 9P2000.L-negotiated Conn.
func (r *Raw) Txattrcreate(ctx context.Context, fid proto.Fid, name string, attrSize uint64, flags uint32) error {
	if err := r.c.requireDialect(protocolL, "Txattrcreate"); err != nil {
		return err
	}
	req := &p9l.Txattrcreate{Fid: fid, Name: name, AttrSize: attrSize, Flags: flags}
	rr, err := rtrip[*p9l.Rxattrcreate](ctx, r.c, req)
	if err != nil {
		return err
	}
	putCachedRMsg(rr)
	return nil
}

// Tlock issues a POSIX byte-range lock operation against fid and returns
// the server's lock status (OK, Blocked, Error, Grace). [proto.LockType]
// is one of LockTypeRdLck / LockTypeWrLck / LockTypeUnlck. Flags may
// request blocking or reclaim semantics; start+length define the byte
// range; procID and clientID identify the lock holder.
//
// Requires a 9P2000.L-negotiated Conn. High-level blocking
// [File.Lock] with ctx-driven poll/backoff wraps this primitive.
func (r *Raw) Tlock(ctx context.Context, fid proto.Fid, lt proto.LockType, flags proto.LockFlags, start, length uint64, procID uint32, clientID string) (proto.LockStatus, error) {
	if err := r.c.requireDialect(protocolL, "Tlock"); err != nil {
		return 0, err
	}
	req := &p9l.Tlock{
		Fid:      fid,
		LockType: lt,
		Flags:    flags,
		Start:    start,
		Length:   length,
		ProcID:   procID,
		ClientID: clientID,
	}
	rr, err := rtrip[*p9l.Rlock](ctx, r.c, req)
	if err != nil {
		return 0, err
	}
	status := rr.Status
	putCachedRMsg(rr)
	return status, nil
}

// Tgetlock tests whether the described POSIX lock could be placed, returning
// the conflicting lock holder's parameters (or LockTypeUnlck when the region
// is free).
//
// The return value is [p9l.Rgetlock] by value -- Rgetlock carries only value-
// typed fields (LockType + uint64x2 + uint32 + string), which are naturally
// passed by value and keep the cache-hit contract consistent (Rgetlock is
// uncached per client/msgcache.go).
//
// Requires a 9P2000.L-negotiated Conn. High-level [File.GetLock]
// wraps this primitive.
func (r *Raw) Tgetlock(ctx context.Context, fid proto.Fid, lt proto.LockType, start, length uint64, procID uint32, clientID string) (p9l.Rgetlock, error) {
	if err := r.c.requireDialect(protocolL, "Tgetlock"); err != nil {
		return p9l.Rgetlock{}, err
	}
	req := &p9l.Tgetlock{
		Fid:      fid,
		LockType: lt,
		Start:    start,
		Length:   length,
		ProcID:   procID,
		ClientID: clientID,
	}
	rr, err := rtrip[*p9l.Rgetlock](ctx, r.c, req)
	if err != nil {
		return p9l.Rgetlock{}, err
	}
	out := *rr
	putCachedRMsg(rr)
	return out, nil
}

// Tstatfs returns filesystem statistics for the file tree containing
// fid. The returned [proto.FSStat] is by value (not pointer): FSStat is
// a small fixed-size struct, value return avoids an escape and keeps
// the cache-lifetime contract local to this method.
//
// Requires a 9P2000.L-negotiated Conn. High-level [File.Statfs] wraps
// this primitive.
func (r *Raw) Tstatfs(ctx context.Context, fid proto.Fid) (proto.FSStat, error) {
	if err := r.c.requireDialect(protocolL, "Tstatfs"); err != nil {
		return proto.FSStat{}, err
	}
	rr, err := rtrip[*p9l.Rstatfs](ctx, r.c, &p9l.Tstatfs{Fid: fid})
	if err != nil {
		return proto.FSStat{}, err
	}
	stat := rr.Stat
	putCachedRMsg(rr)
	return stat, nil
}

// Tfsync flushes fid's data (and, unless dataSync is true, its metadata)
// to storage. fid must be in the opened state -- the server returns
// EBADF for a fid that has never been Tlopen'd.
//
// Requires a 9P2000.L-negotiated Conn. High-level [File.Fsync] wraps
// this primitive.
func (r *Raw) Tfsync(ctx context.Context, fid proto.Fid, dataSync bool) error {
	if err := r.c.requireDialect(protocolL, "Tfsync"); err != nil {
		return err
	}
	var ds uint32
	if dataSync {
		ds = 1
	}
	req := &p9l.Tfsync{Fid: fid, DataSync: ds}
	rr, err := rtrip[*p9l.Rfsync](ctx, r.c, req)
	if err != nil {
		return err
	}
	putCachedRMsg(rr)
	return nil
}

// Tgetattr requests the subset of file attributes selected by mask from fid.
// Callers typically pass [proto.AttrBasic] (0x7ff) for the common case;
// callers who need only Size or Mode can narrow the mask to amortize server
// work. The server is permitted to return MORE than requested.
//
// Requires a 9P2000.L-negotiated Conn. High-level [File.Stat] wraps
// this primitive.
func (r *Raw) Tgetattr(ctx context.Context, fid proto.Fid, mask proto.AttrMask) (proto.Attr, error) {
	if err := r.c.requireDialect(protocolL, "Tgetattr"); err != nil {
		return proto.Attr{}, err
	}
	req := &p9l.Tgetattr{Fid: fid, RequestMask: mask}
	rr, err := rtrip[*p9l.Rgetattr](ctx, r.c, req)
	if err != nil {
		return proto.Attr{}, err
	}
	attr := rr.Attr
	putCachedRMsg(rr)
	return attr, nil
}

// Tsetattr mutates the attributes selected by attr.Valid on the file
// referenced by fid. Unset-bit fields are ignored server-side.
//
// Requires a 9P2000.L-negotiated Conn. High-level
// [File.Chmod]/[File.Chown]/[File.Truncate] wraps this primitive.
func (r *Raw) Tsetattr(ctx context.Context, fid proto.Fid, attr proto.SetAttr) error {
	if err := r.c.requireDialect(protocolL, "Tsetattr"); err != nil {
		return err
	}
	req := &p9l.Tsetattr{Fid: fid, Attr: attr}
	rr, err := rtrip[*p9l.Rsetattr](ctx, r.c, req)
	if err != nil {
		return err
	}
	putCachedRMsg(rr)
	return nil
}

// Trename moves the entry referenced by fid into directory dfid under name.
// Semantically equivalent to the .L Trename wire op (single-fid rename):
// fid keeps pointing at the renamed object, dfid is the target directory.
//
// Requires a 9P2000.L-negotiated Conn. High-level path-rooted
// [Conn.Rename] wraps this primitive.
func (r *Raw) Trename(ctx context.Context, fid, dfid proto.Fid, name string) error {
	if err := r.c.requireDialect(protocolL, "Trename"); err != nil {
		return err
	}
	req := &p9l.Trename{Fid: fid, DirFid: dfid, Name: name}
	rr, err := rtrip[*p9l.Rrename](ctx, r.c, req)
	if err != nil {
		return err
	}
	putCachedRMsg(rr)
	return nil
}

// Trenameat moves oldName in oldDirFid to newName under newDirFid. Unlike
// [Raw.Trename], both source and target dirs are addressed by fid and no
// fid is held against the object being renamed - no open-fid
// invalidation concerns on this path.
//
// Requires a 9P2000.L-negotiated Conn.
func (r *Raw) Trenameat(ctx context.Context, oldDirFid proto.Fid, oldName string, newDirFid proto.Fid, newName string) error {
	if err := r.c.requireDialect(protocolL, "Trenameat"); err != nil {
		return err
	}
	req := &p9l.Trenameat{
		OldDirFid: oldDirFid,
		OldName:   oldName,
		NewDirFid: newDirFid,
		NewName:   newName,
	}
	rr, err := rtrip[*p9l.Rrenameat](ctx, r.c, req)
	if err != nil {
		return err
	}
	putCachedRMsg(rr)
	return nil
}

// Tunlinkat removes the entry name from directory dirFid. Flags may include
// AT_REMOVEDIR (0x200) to request directory removal; otherwise the entry
// must be a non-directory.
//
// Requires a 9P2000.L-negotiated Conn. Higher-level [Conn.Remove]
// wraps this primitive.
func (r *Raw) Tunlinkat(ctx context.Context, dirFid proto.Fid, name string, flags uint32) error {
	if err := r.c.requireDialect(protocolL, "Tunlinkat"); err != nil {
		return err
	}
	req := &p9l.Tunlinkat{DirFid: dirFid, Name: name, Flags: flags}
	rr, err := rtrip[*p9l.Runlinkat](ctx, r.c, req)
	if err != nil {
		return err
	}
	putCachedRMsg(rr)
	return nil
}

// Tlink creates a hard link named name in directory dfid pointing at the
// file referenced by fid. Both fids must resolve within the same file
// tree; cross-mount links are rejected by the server.
//
// Requires a 9P2000.L-negotiated Conn. Higher-level [Conn.Link] wraps
// this primitive.
func (r *Raw) Tlink(ctx context.Context, dfid, fid proto.Fid, name string) error {
	if err := r.c.requireDialect(protocolL, "Tlink"); err != nil {
		return err
	}
	req := &p9l.Tlink{DirFid: dfid, Fid: fid, Name: name}
	rr, err := rtrip[*p9l.Rlink](ctx, r.c, req)
	if err != nil {
		return err
	}
	putCachedRMsg(rr)
	return nil
}

// Tmknod creates a device node named name in directory dfid. Mode carries
// POSIX mode + device-type bits; major/minor select the device; gid sets
// the owning group. Returns the new node's QID.
//
// Requires a 9P2000.L-negotiated Conn. Higher-level [Conn.Mknod] wraps
// this primitive.
func (r *Raw) Tmknod(ctx context.Context, dfid proto.Fid, name string, mode, major, minor, gid uint32) (proto.QID, error) {
	if err := r.c.requireDialect(protocolL, "Tmknod"); err != nil {
		return proto.QID{}, err
	}
	req := &p9l.Tmknod{
		DirFid: dfid,
		Name:   name,
		Mode:   proto.FileMode(mode),
		Major:  major,
		Minor:  minor,
		GID:    gid,
	}
	rr, err := rtrip[*p9l.Rmknod](ctx, r.c, req)
	if err != nil {
		return proto.QID{}, err
	}
	qid := rr.QID
	putCachedRMsg(rr)
	return qid, nil
}

// Tmkdir creates a directory named name in the directory referenced by
// dfid. mode carries the POSIX permission bits; gid sets the owning
// group. Returns the new directory's QID.
//
// Requires a 9P2000.L-negotiated Conn. Higher-level [Conn.Mkdir] wraps
// this primitive.
func (r *Raw) Tmkdir(ctx context.Context, dfid proto.Fid, name string, mode proto.FileMode, gid uint32) (proto.QID, error) {
	if err := r.c.requireDialect(protocolL, "Tmkdir"); err != nil {
		return proto.QID{}, err
	}
	req := &p9l.Tmkdir{DirFid: dfid, Name: name, Mode: mode, GID: gid}
	rr, err := rtrip[*p9l.Rmkdir](ctx, r.c, req)
	if err != nil {
		return proto.QID{}, err
	}
	qid := rr.QID
	putCachedRMsg(rr)
	return qid, nil
}

// Tremove removes the file associated with fid and invalidates the fid.
// The 9P spec states Tremove clunks fid regardless of whether the removal
// succeeded server-side; callers MUST NOT issue a subsequent [Raw.Clunk]
// on this fid, and should release the fid to the allocator via
// [Raw.ReleaseFid] once this call returns (error or not).
//
// Dialect-neutral - Tremove ships on both 9P2000.L and 9P2000.u wire
// sets (see proto/messages.go).
func (r *Raw) Tremove(ctx context.Context, fid proto.Fid) error {
	rr, err := rtrip[*proto.Rremove](ctx, r.c, &proto.Tremove{Fid: fid})
	if err != nil {
		return err
	}
	putCachedRMsg(rr)
	return nil
}

// Tstat returns the .u-format stat structure for the file referenced by
// fid. The 9P2000.u Stat carries the legacy 16-field layout (Name/UID/GID
// strings + numeric IDs in the .u extension fields).
//
// Requires a 9P2000.u-negotiated Conn; returns a wrapped
// [ErrNotSupported] on .L where [Raw.Tgetattr] is the dialect
// equivalent. The unified [File.Stat] picks the correct wire op
// from c.Dialect().
func (r *Raw) Tstat(ctx context.Context, fid proto.Fid) (p9u.Stat, error) {
	if err := r.c.requireDialect(protocolU, "Tstat"); err != nil {
		return p9u.Stat{}, err
	}
	rr, err := rtrip[*p9u.Rstat](ctx, r.c, &p9u.Tstat{Fid: fid})
	if err != nil {
		return p9u.Stat{}, err
	}
	stat := rr.Stat
	putCachedRMsg(rr)
	return stat, nil
}

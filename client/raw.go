package client

import (
	"context"

	"github.com/dotwaffle/ninep/proto"
)

// Raw is the canonical wire surface of a [Conn]: every 9P T-message the
// client can issue appears here as a method named after it (Tattach,
// Twalk, Tread, ... Tstat). Unlike the high-level *File surface, Raw
// takes explicit fid arguments and does NOT track offsets or
// auto-clunk -- callers own fid lifecycle.
//
// Obtain via [Conn.Raw]. Every Raw method issues exactly one T-message
// and blocks for its R-message.
//
// # Concurrency
//
// Raw methods are safe for concurrent use by multiple goroutines -- they
// delegate to Conn, which is goroutine-safe (database/sql.DB model).
// The Conn serializes wire emission via its write
// mutex, and the read loop routes responses by tag. This means N
// concurrent Raw.Twrite calls on the same fid dispatch N Twrite frames
// sequentially on the wire; the wins over sequential round-trips come
// from overlapping server processing, not from wire parallelism.
//
// # Fid ownership
//
// Raw does not call the fid allocator implicitly. Callers that want to
// bypass the *File handle and manage fid lifecycle explicitly must
// supply fid values from their own pool (e.g. a port of an existing 9P
// client that tracks fids in a parallel structure). The AcquireFid /
// ReleaseFid methods integrate with the Conn's allocator.
type Raw struct {
	c *Conn
}

// Raw returns the Raw wire surface for this Conn. The returned pointer
// aliases a field embedded in the Conn, so repeated calls do not
// allocate.
func (c *Conn) Raw() *Raw {
	return &c.raw
}

// Tattach associates fid with the root of the file tree named by aname
// and establishes the session for user uname. Only afid=NoFid (no
// authentication) is supported; Tauth is not implemented. aname selects
// the mount point, server-defined; the empty string is the conventional
// "default" root.
//
// Returns the root QID on success, or a *Error translated from
// Rlerror/Rerror on server-side failure. The high-level [Conn.Attach]
// wraps this to return a *File with an allocator-owned fid.
func (r *Raw) Tattach(ctx context.Context, fid proto.Fid, uname, aname string) (proto.QID, error) {
	return r.c.tattach(ctx, fid, uname, aname)
}

// Twalk descends from fid along names, creating newFid at the final
// element. An empty names slice clones fid into newFid without
// navigating. Returns one QID per successfully walked element; fewer
// QIDs than names is a partial walk, in which case newFid is NOT bound
// server-side.
//
// The returned []proto.QID is caller-owned -- it is copied out of the
// pooled Rwalk struct before the struct is returned to the cache, so
// callers may retain the slice indefinitely.
func (r *Raw) Twalk(ctx context.Context, fid, newFid proto.Fid, names []string) ([]proto.QID, error) {
	return r.c.twalk(ctx, fid, newFid, names)
}

// Tclunk releases fid. After a successful clunk, fid is no longer
// valid; the server deallocates any associated state. Errors from
// Rlerror/Rerror surface as *Error. Callers that allocated the fid from
// the Conn's allocator remain responsible for returning it via
// [Raw.ReleaseFid].
func (r *Raw) Tclunk(ctx context.Context, fid proto.Fid) error {
	return r.c.tclunk(ctx, fid)
}

// Tflush asks the server to abort the request identified by oldTag.
// Per the 9P spec the server responds with Rflush regardless of whether
// oldTag matches an outstanding request, so a nil return does NOT
// confirm the original request was cancelled -- it may have completed
// before the Tflush was received.
//
// High-level callers usually never need this: the dispatch path sends
// Tflush automatically when a request's ctx cancels mid-flight.
func (r *Raw) Tflush(ctx context.Context, oldTag proto.Tag) error {
	return r.c.tflush(ctx, oldTag)
}

// Tread reads up to count bytes from fid starting at offset. Returns
// the bytes actually read, which may be fewer than count (EOF or short
// read). The returned slice is caller-owned and may be retained
// indefinitely.
//
// Tread does NOT clamp count to the negotiated msize or the file's
// iounit. Callers that need throughput-optimal chunking should consult
// the iounit returned by Tlopen/Topen and size their reads accordingly;
// passing an over-large count results in whatever the server chooses to
// return (many servers clamp silently).
func (r *Raw) Tread(ctx context.Context, fid proto.Fid, offset uint64, count uint32) ([]byte, error) {
	return r.c.tread(ctx, fid, offset, count)
}

// Twrite writes data to fid starting at offset. Returns the number of
// bytes the server reports as written (may be fewer than len(data)).
func (r *Raw) Twrite(ctx context.Context, fid proto.Fid, offset uint64, data []byte) (uint32, error) {
	return r.c.twrite(ctx, fid, offset, data)
}

// Tlopen opens an existing file referenced by fid with the given POSIX
// open flags (O_RDONLY, O_RDWR, etc.). Requires a 9P2000.L-negotiated
// Conn; on a .u Conn returns [ErrNotSupported] without touching the
// wire.
//
// Returns the file's QID and the server's suggested iounit (the maximum
// bytes the server is willing to return in a single Rread or accept in
// a single Twrite; a value of 0 means "unknown, use msize").
func (r *Raw) Tlopen(ctx context.Context, fid proto.Fid, flags uint32) (proto.QID, uint32, error) {
	return r.c.tlopen(ctx, fid, flags)
}

// Tlcreate creates and opens a new file named name in the directory
// referenced by fid. After a successful Tlcreate, fid is mutated
// server-side to refer to the newly-created file (not the parent
// directory); this matches Plan 9 and the Linux v9fs kernel client.
// Requires a .L-negotiated Conn.
//
// flags is the POSIX open flag set (O_RDWR, O_CREAT already implied,
// etc.). mode is the POSIX permission bits + file-type. gid is the
// group to assign to the new file (zero for "use the server default").
func (r *Raw) Tlcreate(ctx context.Context, fid proto.Fid, name string, flags uint32, mode proto.FileMode, gid uint32) (proto.QID, uint32, error) {
	return r.c.tlcreate(ctx, fid, name, flags, mode, gid)
}

// Topen is the 9P2000.u file-open operation. Requires a .u-negotiated
// Conn; on a .L Conn returns [ErrNotSupported].
//
// mode is a 9P2000.u open mode (OREAD=0, OWRITE=1, ORDWR=2, OEXEC=3
// with optional flag bits in the upper bits). Returns QID + iounit.
func (r *Raw) Topen(ctx context.Context, fid proto.Fid, mode uint8) (proto.QID, uint32, error) {
	return r.c.topen(ctx, fid, mode)
}

// Tcreate is the 9P2000.u create-and-open wire operation. Requires a
// .u-negotiated Conn. The high-level [Conn.Create] wraps this and the
// .L-only [Raw.Tlcreate] behind a dialect-neutral session method; use
// Tcreate only when explicit fid control is needed.
//
// perm is the file-mode + type bits; mode is the 9P2000.u open mode;
// extension is the .u Extension field (symlink target, device spec,
// etc. -- empty for regular files).
func (r *Raw) Tcreate(ctx context.Context, fid proto.Fid, name string, perm proto.FileMode, mode uint8, extension string) (proto.QID, uint32, error) {
	return r.c.tcreate(ctx, fid, name, perm, mode, extension)
}

// AcquireFid hands out a fresh fid from the Conn's allocator. Callers
// that use Raw for explicit fid lifecycle pair each AcquireFid with
// either a successful [Raw.Tclunk] + [Raw.ReleaseFid] sequence, or a
// [Raw.ReleaseFid] alone if the fid never became server-bound (e.g.
// Twalk/Tlopen failed before the server registered the new fid).
//
// Returns [ErrFidExhausted] if the per-Conn counter has run past
// proto.NoFid.
func (r *Raw) AcquireFid() (proto.Fid, error) {
	return r.c.fids.acquire()
}

// ReleaseFid returns fid to the Conn's allocator reuse cache. Does not
// touch the wire; pair with [Raw.Tclunk] when the fid is server-bound.
//
// Ordering: when fid IS server-bound, callers MUST wait for the
// corresponding Rclunk to be received BEFORE calling ReleaseFid - a
// released-then-reused fid before the Rclunk lands races with the
// next Twalk on the same fid number.
func (r *Raw) ReleaseFid(fid proto.Fid) {
	r.c.fids.release(fid)
}

package client

import (
	"context"

	"github.com/dotwaffle/ninep/proto"
)

// Raw exposes direct 9P wire operations against a [Conn]. Unlike the
// high-level *File surface, Raw takes explicit fid arguments and does
// NOT track offsets or auto-clunk -- callers own fid lifecycle.
//
// Obtain via [Conn.Raw]. Every Raw method issues exactly one T-message
// and blocks for its R-message.
//
// # Concurrency
//
// Raw methods are safe for concurrent use by multiple goroutines -- they
// delegate to Conn, which is goroutine-safe (database/sql.DB model per
// Phase 19 D-07). The Conn serializes wire emission via its write
// mutex, and the read loop routes responses by tag. This means N
// concurrent Raw.Write calls on the same fid dispatch N Twrite frames
// sequentially on the wire; the wins over sequential round-trips come
// from overlapping server processing, not from wire parallelism.
//
// # Fid ownership
//
// Raw does not call the fid allocator. Callers that want to bypass the
// *File handle and manage fid lifecycle explicitly must supply fid
// values from their own pool (e.g. a port of an existing 9P client
// that tracks fids in a parallel structure). Plan 20-03 will expose
// AcquireFid/ReleaseFid to integrate with the Conn's allocator.
type Raw struct {
	c *Conn
}

// Raw returns the Raw sub-surface for this Conn. The returned value is a
// thin wrapper -- repeated calls do not allocate beyond the returned
// pointer and methods on the returned Raw delegate 1:1 to the
// corresponding [Conn] methods.
func (c *Conn) Raw() *Raw {
	return &Raw{c: c}
}

// Attach mirrors [Conn.Attach]. Caller supplies fid.
func (r *Raw) Attach(ctx context.Context, fid proto.Fid, uname, aname string) (proto.QID, error) {
	return r.c.Attach(ctx, fid, uname, aname)
}

// Walk mirrors [Conn.Walk]. An empty names slice clones fid into newFid
// without navigating.
func (r *Raw) Walk(ctx context.Context, fid, newFid proto.Fid, names []string) ([]proto.QID, error) {
	return r.c.Walk(ctx, fid, newFid, names)
}

// Clunk mirrors [Conn.Clunk]. Releases the server-side fid binding;
// callers that allocated the fid from the Conn's allocator remain
// responsible for returning it to the allocator (Plan 20-03 wires
// this).
func (r *Raw) Clunk(ctx context.Context, fid proto.Fid) error {
	return r.c.Clunk(ctx, fid)
}

// Flush mirrors [Conn.Flush]. Sends Tflush(oldTag); per the 9P spec the
// server always replies with Rflush regardless of whether oldTag
// matched an outstanding request.
func (r *Raw) Flush(ctx context.Context, oldTag proto.Tag) error {
	return r.c.Flush(ctx, oldTag)
}

// Read mirrors [Conn.Read]. The returned slice is caller-owned.
func (r *Raw) Read(ctx context.Context, fid proto.Fid, offset uint64, count uint32) ([]byte, error) {
	return r.c.Read(ctx, fid, offset, count)
}

// Write mirrors [Conn.Write].
func (r *Raw) Write(ctx context.Context, fid proto.Fid, offset uint64, data []byte) (uint32, error) {
	return r.c.Write(ctx, fid, offset, data)
}

// Lopen mirrors [Conn.Lopen]. Requires a 9P2000.L-negotiated Conn; on
// a .u Conn returns [ErrNotSupported].
func (r *Raw) Lopen(ctx context.Context, fid proto.Fid, flags uint32) (proto.QID, uint32, error) {
	return r.c.Lopen(ctx, fid, flags)
}

// Lcreate mirrors [Conn.Lcreate]. Requires a 9P2000.L-negotiated Conn.
func (r *Raw) Lcreate(ctx context.Context, fid proto.Fid, name string, flags uint32, mode proto.FileMode, gid uint32) (proto.QID, uint32, error) {
	return r.c.Lcreate(ctx, fid, name, flags, mode, gid)
}

// Open mirrors [Conn.Open]. Requires a 9P2000.u-negotiated Conn; on
// a .L Conn returns [ErrNotSupported].
func (r *Raw) Open(ctx context.Context, fid proto.Fid, mode uint8) (proto.QID, uint32, error) {
	return r.c.Open(ctx, fid, mode)
}

// Create mirrors [Conn.Create]. Requires a 9P2000.u-negotiated Conn.
func (r *Raw) Create(ctx context.Context, fid proto.Fid, name string, perm proto.FileMode, mode uint8, extension string) (proto.QID, uint32, error) {
	return r.c.Create(ctx, fid, name, perm, mode, extension)
}

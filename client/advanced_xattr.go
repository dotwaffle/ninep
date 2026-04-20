package client

import (
	"context"
	"fmt"
	"strings"

	"github.com/dotwaffle/ninep/proto"
)

// Phase 21 Plan 03 — high-level extended-attribute surface on *File.
//
// Each method hides the 9P2000.L two-phase xattr protocol:
//
//   - XattrGet / XattrList: Txattrwalk (allocates a dedicated read fid
//     bound to the source file + name; name="" lists all attrs) →
//     Tread-loop draining the server-declared size → Tclunk releases
//     the xattr fid. The caller's *File is untouched.
//
//   - XattrSet / XattrRemove: File.Clone (produces a disposable fid)
//     → Txattrcreate (server MUTATES the cloned fid into xattr-write
//     state — Pitfall 1) → Twrite loop → Tclunk commits. The caller's
//     *File is again untouched because the mutation lands on the
//     clone, not on f.
//
// All four methods are 9P2000.L-only; .u returns ErrNotSupported via
// the requireDialect gate before any wire op.

// xattrChunk returns the maximum byte count safe to transfer in a
// single Tread/Twrite for an xattr op. Mirrors File.maxChunk's msize
// clamp — xattr ops do not carry an iounit, so the only bound is
// msize minus per-message framing overhead.
//
// Returns 0 when the Conn's negotiated msize is at or below the
// framing overhead — a state only reachable against a pathological
// mock server ([Dial] enforces minMsize=256 > ioFrameOverhead).
// Callers MUST treat chunk==0 as "no forward progress possible" and
// surface a clear error before entering the Tread/Twrite loop; zero
// must never be fed to r.Read/r.Write where it would mask as a
// short-read/write.
func (f *File) xattrChunk() uint32 {
	m := f.conn.Msize()
	if m <= ioFrameOverhead {
		return 0
	}
	return m - ioFrameOverhead
}

// twoPhaseXattrRead is the Txattrwalk → Tread-loop → Tclunk
// choreography shared by XattrGet and XattrList (the latter uses
// name="" per 9P2000.L convention).
//
// Pitfall 2 bound: the Rxattrwalk.Size field is an 8-byte server-
// supplied integer. A malicious or broken server could declare any
// value up to 2^64-1; making a []byte of that size would OOM the
// client. Clamp against proto.MaxDataSize (16 MiB) BEFORE allocation
// and surface an error without draining a single Tread byte.
//
// On every exit path the freshly-allocated xattr fid is clunked
// (best-effort) and released to the fid allocator. Errors from the
// cleanup Clunk are suppressed — the primary error is what the caller
// needs to see, and the fid is returned to the allocator regardless.
func (f *File) twoPhaseXattrRead(ctx context.Context, name string) ([]byte, error) {
	if err := f.conn.requireDialect(protocolL, "XattrGet"); err != nil {
		return nil, err
	}
	newFid, err := f.conn.fids.acquire()
	if err != nil {
		return nil, err
	}
	r := f.conn.Raw()
	size, err := r.Txattrwalk(ctx, f.fid, newFid, name)
	if err != nil {
		// Txattrwalk failed → server never bound newFid. No Clunk.
		f.conn.fids.release(newFid)
		return nil, err
	}
	// Pitfall 2: reject oversized server declarations before allocation.
	if size > uint64(proto.MaxDataSize) {
		_ = r.Clunk(ctx, newFid)
		f.conn.fids.release(newFid)
		return nil, fmt.Errorf("client: xattr size %d exceeds MaxDataSize %d", size, proto.MaxDataSize)
	}
	if size == 0 {
		// Zero-size short-circuit: no Tread round-trips, just clunk and
		// return a non-nil empty slice (matches Linux getxattr(2)'s
		// "attribute exists with empty value" semantics).
		if err := r.Clunk(ctx, newFid); err != nil {
			f.conn.fids.release(newFid)
			return nil, err
		}
		f.conn.fids.release(newFid)
		return []byte{}, nil
	}

	buf := make([]byte, size)
	off := uint64(0)
	chunk := f.xattrChunk()
	if chunk == 0 {
		// Conn msize is at or below the per-message framing floor
		// (only reachable against a pathological mock server — Dial
		// enforces minMsize=256). No forward progress is possible.
		_ = r.Clunk(ctx, newFid)
		f.conn.fids.release(newFid)
		return nil, fmt.Errorf("client: xattr chunk is 0 (msize %d ≤ ioFrameOverhead %d)",
			f.conn.Msize(), ioFrameOverhead)
	}
	for off < size {
		remaining := size - off
		want := uint32(chunk)
		if uint64(want) > remaining {
			want = uint32(remaining)
		}
		data, err := r.Read(ctx, newFid, off, want)
		if err != nil {
			_ = r.Clunk(ctx, newFid)
			f.conn.fids.release(newFid)
			return nil, err
		}
		if len(data) == 0 {
			// Server promised `size` bytes via Rxattrwalk but returned
			// zero at offset off — short read, surface as an error.
			_ = r.Clunk(ctx, newFid)
			f.conn.fids.release(newFid)
			return nil, fmt.Errorf("client: xattr short read at offset %d/%d", off, size)
		}
		// Guard against an over-read: the server must not return more
		// bytes than we asked for (want, which itself is clamped to
		// remaining). copy() already bounds against len(buf[off:]) so
		// the backing array is safe, but advancing off by len(data)
		// would silently truncate the tail. Surface as an error.
		if uint64(len(data)) > remaining {
			_ = r.Clunk(ctx, newFid)
			f.conn.fids.release(newFid)
			return nil, fmt.Errorf("client: xattr over-read: server returned %d bytes at offset %d, only %d remaining",
				len(data), off, remaining)
		}
		copy(buf[off:], data)
		off += uint64(len(data))
	}
	if err := r.Clunk(ctx, newFid); err != nil {
		f.conn.fids.release(newFid)
		return nil, err
	}
	f.conn.fids.release(newFid)
	return buf, nil
}

// XattrGet retrieves the value of the extended attribute name. Hides
// the 9P2000.L two-phase protocol (Txattrwalk → Tread loop → Tclunk).
//
// Returns a non-nil, zero-length []byte when the attribute exists with
// an empty value; returns a *[Error] wrapping [proto.ENODATA] when the
// attribute does not exist on the file. Use [errors.Is] against
// proto.ENODATA for the missing-attribute case:
//
//	v, err := f.XattrGet(ctx, "user.comment")
//	if errors.Is(err, proto.ENODATA) {
//	    // attribute not set
//	}
//
// The server-declared size is clamped against [proto.MaxDataSize]
// (16 MiB) before the client allocates its receive buffer — a
// misbehaving server cannot OOM this call by declaring an oversized
// value.
//
// Requires a 9P2000.L-negotiated Conn. Returns [ErrNotSupported]
// (wrapped with op context) on a .u Conn before touching the wire.
func (f *File) XattrGet(ctx context.Context, name string) ([]byte, error) {
	return f.twoPhaseXattrRead(ctx, name)
}

// XattrSet writes data as the value of the extended attribute name.
// flags follows Linux setxattr(2): 0 = create-or-replace, XATTR_CREATE
// (1) fails if the attribute already exists, XATTR_REPLACE (2) fails
// if it does not. The exact flag semantics are enforced by the server
// implementation of [server.NodeXattrSetter].
//
// Pitfall 1: the 9P2000.L Txattrcreate protocol MUTATES the supplied
// fid into xattr-write state; after the subsequent Tclunk commits the
// value, that fid is invalidated. To keep the caller's *File f valid
// for reads/writes after XattrSet returns, this method clones f first
// (File.Clone allocates a fresh fid at the same server-side node) and
// performs the mutation on the clone.
//
// The total byte count of data must fit within the server's negotiated
// msize (server/bridge.go:897 clamps Txattrcreate.AttrSize to msize);
// callers with larger xattrs should either raise msize at Dial time
// via [WithMsize] or use [Raw.Txattrcreate] directly.
//
// Requires a 9P2000.L-negotiated Conn.
//
// Performance Note: XattrSet clones the current file internally to
// preserve the fid's open/offset state. This consumes one transient fid.
func (f *File) XattrSet(ctx context.Context, name string, data []byte, flags uint32) error {
	if err := f.conn.requireDialect(protocolL, "XattrSet"); err != nil {
		return err
	}
	// Pitfall 1: operate on a clone so f.fid is untouched by the
	// xattrcreate-write-clunk sequence.
	clone, err := f.Clone(ctx)
	if err != nil {
		return err
	}
	// Do NOT `defer clone.Close()` — the final Raw.Clunk below is the
	// xattr-commit gesture; clone.Close would double-clunk. Explicit
	// fid release after the commit keeps the ownership model obvious.

	r := f.conn.Raw()
	size := uint64(len(data))
	if err := r.Txattrcreate(ctx, clone.fid, name, size, flags); err != nil {
		// Txattrcreate failed → server state unchanged. Clone's fid is
		// still in the walked state, so a normal Clunk is the right
		// release gesture.
		_ = r.Clunk(ctx, clone.fid)
		f.conn.fids.release(clone.fid)
		return err
	}

	// Twrite loop. For size == 0 (XattrRemove's composed call), the
	// loop body executes zero times and we drop straight into Clunk,
	// which triggers the server's remove-on-commit path
	// (server/dispatch.go:254-261).
	chunk := f.xattrChunk()
	if chunk == 0 && size > 0 {
		// Conn msize is at or below the per-message framing floor
		// (only reachable against a pathological mock server — Dial
		// enforces minMsize=256). No forward progress is possible.
		_ = r.Clunk(ctx, clone.fid)
		f.conn.fids.release(clone.fid)
		return fmt.Errorf("client: xattr chunk is 0 (msize %d ≤ ioFrameOverhead %d)",
			f.conn.Msize(), ioFrameOverhead)
	}
	off := uint64(0)
	for off < size {
		end := min(off+uint64(chunk), size)
		n, err := r.Write(ctx, clone.fid, off, data[off:end])
		if err != nil {
			// Must still Clunk to release server state; the server
			// validates written==declared and returns EIO on mismatch,
			// which is expected here — swallow that secondary error.
			_ = r.Clunk(ctx, clone.fid)
			f.conn.fids.release(clone.fid)
			return err
		}
		if n == 0 {
			_ = r.Clunk(ctx, clone.fid)
			f.conn.fids.release(clone.fid)
			return fmt.Errorf("client: xattr short write at offset %d/%d", off, size)
		}
		off += uint64(n)
	}
	// Tclunk commits: server runs SetXattr(name, data) (or RemoveXattr
	// when size==0). Any error here is a real commit failure and must
	// surface to the caller.
	if err := r.Clunk(ctx, clone.fid); err != nil {
		f.conn.fids.release(clone.fid)
		return err
	}
	f.conn.fids.release(clone.fid)
	return nil
}

// XattrList returns the names of all extended attributes set on this
// file. Composes XattrGet with name="" per the 9P2000.L list-all
// convention — the server returns attribute names NUL-separated with
// a trailing NUL byte after the last name.
//
// Returns a non-nil empty slice (not nil) when no attributes exist,
// so callers can treat the return value uniformly without a nil check
// before ranging.
//
// The trailing NUL and any internal empty entries (a defensive guard
// against malformed server responses — no legitimate xattr name is
// the empty string) are filtered out.
//
// Requires a 9P2000.L-negotiated Conn.
func (f *File) XattrList(ctx context.Context) ([]string, error) {
	data, err := f.twoPhaseXattrRead(ctx, "")
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return []string{}, nil
	}
	parts := strings.Split(string(data), "\x00")
	// Filter empties: drops the trailing NUL's empty entry and any
	// stray middle empties from a misbehaving server.
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// XattrRemove deletes the extended attribute name. Composes XattrSet
// with a zero-length value and flags=0 per the 9P2000.L wire
// convention — the server's xattr-commit path
// (server/dispatch.go:254) treats size=0 at commit time as a request
// to invoke [server.NodeXattrRemover].
//
// Requires a 9P2000.L-negotiated Conn. The server returns an error
// wrapping [proto.ENODATA] if name does not exist, matching
// removexattr(2) semantics; callers can discriminate with
// [errors.Is](err, proto.ENODATA).
func (f *File) XattrRemove(ctx context.Context, name string) error {
	return f.XattrSet(ctx, name, nil, 0)
}

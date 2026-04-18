package client

import (
	"context"
	"fmt"
	"os"

	"github.com/dotwaffle/ninep/proto"
)

// Attach binds this Conn to a filesystem mount. Issues Tattach(rootFid,
// NoFid, uname, aname) where rootFid is freshly allocated from this
// Conn's fid pool. Returns a [*File] representing the root directory.
//
// Authentication (afid != NoFid) is not supported in v1.3.0 -- see
// package doc. Attach always passes NoFid for the afid field; use
// [Conn.AttachFid] (or [Raw.Attach]) if wire-level control is needed.
//
// The returned *File is also cached on the Conn as the implicit root
// for subsequent [Conn.OpenFile] / [Conn.Create] calls. Callers may
// Attach multiple times; the most recent successful Attach becomes the
// new implicit root (the prior root File is NOT auto-Closed -- callers
// that care track both).
//
// Per D-05 / D-19: ctx is respected for the Tattach round-trip.
func (c *Conn) Attach(ctx context.Context, uname, aname string) (*File, error) {
	fid, err := c.fids.acquire()
	if err != nil {
		return nil, err
	}
	qid, err := c.AttachFid(ctx, fid, uname, aname)
	if err != nil {
		c.fids.release(fid)
		return nil, err
	}
	f := newFile(c, fid, qid, 0)
	c.root.Store(f)
	return f, nil
}

// Root returns the *File from the most recent successful [Conn.Attach].
// Returns nil if no Attach has ever succeeded on this Conn. Does NOT
// issue a wire op.
func (c *Conn) Root() *File {
	v := c.root.Load()
	if v == nil {
		return nil
	}
	f, ok := v.(*File)
	if !ok {
		return nil
	}
	return f
}

// OpenFile walks from the root (set by a prior [Conn.Attach]) to path,
// then opens the walked-to fid with the given POSIX-style flags and
// mode. Signature mirrors [os.OpenFile] for caller familiarity.
//
// On a 9P2000.L Conn, issues Tlopen with flags passed through verbatim
// (they are POSIX open flags that .L accepts directly). On a 9P2000.u
// Conn, translates flags to 9P2000.u mode bits (O_RDONLY -> 0,
// O_WRONLY -> 1, O_RDWR -> 2; higher POSIX bits dropped) and issues
// Topen.
//
// The mode parameter is currently unused by OpenFile (it is reserved
// for Create-style call signatures parallelism); the created/opened
// permission bits on Create pass through [Conn.Create]'s perm argument
// instead.
//
// Error-path fid lifecycle (Pitfall 2 / Pitfall 3):
//   - Walk fails BEFORE server binding: reserved fid is released.
//   - Walk succeeds, Lopen/Topen fails: walked fid is Tclunked then
//     released.
func (c *Conn) OpenFile(ctx context.Context, p string, flags int, mode os.FileMode) (*File, error) {
	_ = mode // reserved; non-Create callers leave mode at 0.
	root := c.Root()
	if root == nil {
		return nil, fmt.Errorf("client: OpenFile requires a prior Attach")
	}
	names := splitPath(p)
	fileFid, err := c.fids.acquire()
	if err != nil {
		return nil, err
	}
	qids, err := c.Walk(ctx, root.fid, fileFid, names)
	if err != nil {
		// Walk returned a wire/server error; per 9P spec newFid is not
		// bound unless len(qids) == len(names). Release only.
		c.fids.release(fileFid)
		return nil, err
	}
	if len(names) > 0 && len(qids) != len(names) {
		// Partial walk -- newFid not bound server-side. Release only.
		c.fids.release(fileFid)
		return nil, fmt.Errorf("client: partial walk (%d of %d steps)", len(qids), len(names))
	}

	var qid proto.QID
	var iounit uint32
	switch c.dialect {
	case protocolL:
		qid, iounit, err = c.Lopen(ctx, fileFid, uint32(flags))
	case protocolU:
		qid, iounit, err = c.Open(ctx, fileFid, posixToNinepMode(flags))
	default:
		err = fmt.Errorf("client: unknown dialect %v", c.dialect)
	}
	if err != nil {
		// Walk succeeded -> fileFid is server-bound. Clunk before
		// release (Pitfall 3). Use context.Background() for the
		// cleanup clunk because the caller's ctx may already be
		// cancelled.
		_ = c.Clunk(context.Background(), fileFid)
		c.fids.release(fileFid)
		return nil, err
	}
	return newFile(c, fileFid, qid, iounit), nil
}

// Create walks from the root to the parent directory of path, then
// issues Tlcreate (.L) or Tcreate (.u) for the basename. Returns an
// open *File positioned at offset 0.
//
// On .L, gid defaults to 0 ("server default"). On .u, extension is
// empty (regular file). Callers needing non-default gid or .u
// extensions should use [Raw.Lcreate] / [Raw.Create] directly.
//
// Only the permission bits of mode (mode & 0o7777) are honored;
// [os.FileMode] type bits (os.ModeDir, os.ModeSymlink, os.ModeSetuid,
// etc.) are masked off before the value is encoded on the wire. The
// os.FileMode type-bit encoding does not align with 9P's DM* bits
// (e.g. os.ModeSymlink is 1<<26 while DMSYMLINK is 1<<25), so
// forwarding them verbatim would corrupt the wire mode silently.
// Callers needing to create non-regular files should use the dedicated
// ops (Tmkdir/Tsymlink, wired in a later phase).
//
// After a successful Lcreate/Tcreate, the fid that was walked to the
// parent is mutated server-side to refer to the newly-created file
// (9P semantics). The returned *File wraps that same fid.
func (c *Conn) Create(ctx context.Context, p string, flags int, mode os.FileMode) (*File, error) {
	root := c.Root()
	if root == nil {
		return nil, fmt.Errorf("client: Create requires a prior Attach")
	}
	full := splitPath(p)
	if len(full) == 0 {
		return nil, fmt.Errorf("client: Create requires a non-root path")
	}
	parents := full[:len(full)-1]
	name := full[len(full)-1]

	dirFid, err := c.fids.acquire()
	if err != nil {
		return nil, err
	}
	qids, err := c.Walk(ctx, root.fid, dirFid, parents)
	if err != nil {
		c.fids.release(dirFid)
		return nil, err
	}
	if len(parents) > 0 && len(qids) != len(parents) {
		c.fids.release(dirFid)
		return nil, fmt.Errorf("client: partial walk to parent (%d of %d steps)", len(qids), len(parents))
	}

	// Mask to permission bits (0o7777). os.FileMode type bits do not
	// align with 9P's DM* family -- see godoc above. Higher-layer
	// creation of directories / symlinks goes through dedicated ops.
	perm := proto.FileMode(uint32(mode) & 0o7777)
	var qid proto.QID
	var iounit uint32
	switch c.dialect {
	case protocolL:
		qid, iounit, err = c.Lcreate(ctx, dirFid, name, uint32(flags), perm, 0)
	case protocolU:
		qid, iounit, err = c.CreateFid(ctx, dirFid, name, perm, posixToNinepMode(flags), "")
	default:
		err = fmt.Errorf("client: unknown dialect %v", c.dialect)
	}
	if err != nil {
		// Walk succeeded -> dirFid is server-bound (to the parent dir,
		// since create mutates on success only). Clunk before release.
		_ = c.Clunk(context.Background(), dirFid)
		c.fids.release(dirFid)
		return nil, err
	}
	// Post-Lcreate/Tcreate: dirFid now refers to the newly-created
	// file (9P spec + Linux v9fs kernel behavior).
	return newFile(c, dirFid, qid, iounit), nil
}

// OpenDir walks from the Attach'd root to p and opens it as a
// directory. Convenience wrapper over [Conn.OpenFile] with flags=0,
// mode=0; returns an opened *File suitable for [File.ReadDir].
//
// The returned *File must be Closed by the caller. On .L, flags=0
// (== O_RDONLY) opens the directory fid for Tread/Treaddir. On .u,
// [posixToNinepMode] maps 0 to OREAD which likewise opens for
// read-only directory enumeration (though Phase 20's [File.ReadDir]
// is .L-only -- .u directory enumeration is deferred; see Q4).
//
// Empty path, "/", and "." all open the root directory (equivalent to
// a zero-step Twalk followed by Tlopen/Topen).
func (c *Conn) OpenDir(ctx context.Context, p string) (*File, error) {
	return c.OpenFile(ctx, p, 0, 0)
}

// posixToNinepMode maps POSIX O_RDONLY/O_WRONLY/O_RDWR to 9P2000.u's
// mode byte (0/1/2). Higher POSIX flags (O_CREAT, O_TRUNC, O_APPEND,
// O_EXCL) are dropped here -- .u's semantics for those are sparse and
// .u callers that need them should use [Raw.Create] / [Raw.Open]
// directly.
//
// Input is an int because os.O_* are int; output is uint8 because .u
// mode is one byte on the wire.
func posixToNinepMode(flags int) uint8 {
	switch flags & (os.O_RDONLY | os.O_WRONLY | os.O_RDWR) {
	case os.O_WRONLY:
		return 1
	case os.O_RDWR:
		return 2
	default:
		return 0 // O_RDONLY (=0 on POSIX) or unrecognized: treat as read-only.
	}
}

// Package client implements a 9P2000.L/9P2000.u wire-level client over any
// [net.Conn]. The primary surface is the [Conn] type, a multiplexed connection
// that dispatches responses by tag so callers can issue concurrent requests
// without external synchronization.
//
// # Concurrency Model
//
// Conn is safe for concurrent use by multiple goroutines, modeled on
// [database/sql.DB]. A single read goroutine per Conn decodes R-messages and
// delivers each to the waiting caller via a per-tag response channel. Writes
// are serialized with a mutex around [internal/wire.WriteFramesLocked]. Natural
// back-pressure on the bounded tag free-list (see [WithMaxInflight]) caps the
// in-flight request count without a separate semaphore.
//
// # Authentication Scope
//
// This package supports [Tattach] with afid=NoFid only. The Tauth/afid
// handshake is not implemented — the common case (Q, Linux v9fs default
// trans=tcp) is no-auth, and every concrete consumer known at v1.3.0 falls in
// that bucket. Future milestones may add Tauth if a concrete consumer requires
// it.
//
// # Dialects: .L Primary, .u Best-Effort
//
// 9P2000.L is the primary dialect and has full feature parity — every
// operation in the client (attach, walk, open, read, write, clunk, flush) and
// the advanced operations (symlinks, xattr, locks, statfs, rename) are
// implemented for .L.
//
// 9P2000.u is best-effort. The operations with a .u equivalent (Twalk, Tread,
// Twrite, Tclunk, Tcreate, Tstat, Twstat, Tremove, Tflush, Tversion, Tattach)
// work against a .u-negotiated Conn. The .L-only operations (Tgetattr,
// Tsetattr, Tlopen, Tlcreate, Txattrwalk/Txattrcreate, Tlock/Tgetlock,
// Treadlink, Tmknod, Tsymlink, Tlink, Trename, Trenameat, Tunlinkat, Tstatfs)
// return [ErrNotSupported] on a .u-negotiated Conn.
//
// The dialect is chosen by auto-detect: the Conn proposes 9P2000.L and
// downgrades to 9P2000.u if the server's Rversion carries that string.
//
// # Default msize
//
// The default proposed msize is 1 MiB (1 << 20). This matches the Linux
// kernel's v9fs client default so that `mount -t 9p -o trans=tcp` against a
// non-ninep server does not silently downsize to a mismatched message size.
// Override with [WithMsize]. The server's Rversion msize caps the proposal;
// the negotiated msize is the minimum of the two.
//
// Note that the ninep server's default maximum msize is 4 MiB — the asymmetry
// is intentional. Server-to-server callers (e.g. ninep→ninep local) can bump
// with [WithMsize] if profiling shows a win.
//
// # Errors
//
// 9P error responses from the server are surfaced as a *[Error] value that
// wraps a [proto.Errno]. Both Rlerror (9P2000.L) and Rerror (9P2000.u) decode
// to this same type; callers match with the idiomatic Go pattern:
//
//	if errors.Is(err, proto.EACCES) {
//	    // ...
//	}
//
// Use proto.Errno constants rather than syscall.Errno for portability — the
// proto↔syscall bridge is platform-specific (see [Error.Is] godoc).
package client

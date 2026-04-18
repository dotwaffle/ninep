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
//
// # File Handle
//
// The [File] type is the primary high-level API for 9P file operations.
// It implements [io.Reader], [io.Writer], [io.Closer], [io.Seeker],
// [io.ReaderAt], and [io.WriterAt] — so any Go package that consumes
// those interfaces (io.Copy, bufio, encoding/json, compress/gzip,
// net/http Body) can read from and write to 9P files directly without
// adapter code.
//
// Obtain a *File via [Conn.Attach] (root of the filesystem),
// [Conn.OpenFile] (open by path), [Conn.Create] (create and open),
// [Conn.OpenDir] (open directory for enumeration via [File.ReadDir]),
// [File.Walk] (navigate to a child without opening), or [File.Clone]
// (duplicate at the same server-side node for parallel I/O). Every
// *File must be closed via [File.Close]; second calls to Close are a
// no-op returning nil (the intentional deviation from os.File
// semantics is documented on File.Close).
//
// Fid lifecycle is managed implicitly. Callers never see proto.Fid
// values on the high-level path — [Conn.Attach] allocates the root
// fid, [Conn.OpenFile] allocates per-open fids, [File.Clone]
// allocates clone fids, and [File.Close] releases them on clunk.
//
// # Seek semantics
//
// [File.Seek] is a pure client-side arithmetic operation. 9P's
// Tread/Twrite carry the offset on every request, so there is no
// server-side seek state. SeekStart and SeekCurrent never touch the
// wire. SeekEnd uses a cached size field populated by [File.Sync],
// which issues Tgetattr on .L (or Tstat on .u) to refresh. Callers
// that need SeekEnd relative to fresh size after concurrent writes
// invoke Sync first; SeekEnd on a file whose size has not been cached
// returns 0 for SeekEnd(0) and an error guiding the caller to Sync
// for any negative offset.
//
// # Concurrency and parallelism
//
// Each *File has a private mutex that serializes Read, Write, ReadAt,
// and WriteAt calls on the same handle. Callers wanting parallel I/O
// on the same server-side file spawn a [File.Clone] per goroutine;
// each clone has its own fid, its own offset, and its own mutex. The
// underlying Conn is goroutine-safe per database/sql.DB semantics, so
// N clones can issue N in-flight requests that overlap on the server.
//
// # Advanced Operations
//
// Phase 21 adds the 9P operations beyond read/write/walk/open/clunk:
// symbolic links, extended attributes, POSIX locks, filesystem
// statistics, and path manipulation (rename / remove / link / mknod /
// setattr).
//
// ## Symbolic Links
//
//   - [Conn.Symlink] — create a symlink at a path. (.L-only)
//   - [File.Readlink] — read a symlink's target. (.L-only)
//
// ## Extended Attributes (.L-only)
//
//   - [File.XattrGet] / [File.XattrSet] / [File.XattrList] /
//     [File.XattrRemove]
//   - All four hide the 9P two-phase protocol (Txattrwalk → Tread-loop
//     → Tclunk for get; Clone + Txattrcreate + Twrite + Tclunk for
//     set).
//   - Callers needing streaming access use [Raw.Txattrwalk] directly.
//
// ## POSIX Locks (.L-only)
//
//   - [File.Lock] / [File.Unlock] / [File.TryLock] / [File.GetLock]
//   - Blocking Lock uses exponential backoff (10ms → 500ms cap); tests
//     can override via [WithLockPollSchedule].
//   - Ctx cancellation unconditionally releases any granted lock via a
//     background-ctx Tlock(UNLCK) — belt-and-braces against the
//     cancel-during-grant race.
//
// ## Filesystem Statistics
//
//   - [File.Stat] — dialect-neutral metadata (p9u.Stat on both .L and
//     .u). On .L, internally uses Tgetattr; on .u, uses Tstat.
//   - [File.Getattr] — rich .L-specific [proto.Attr] (includes NLink,
//     Blocks, BTime, Gen, DataVersion dropped by Stat). (.L-only)
//   - [File.Setattr] — write metadata (chmod/chown/truncate via
//     [proto.SetAttr].Valid bitmask). (.L-only)
//   - [File.Statfs] — filesystem-level stats (by value, not pointer).
//     (.L-only)
//   - [File.Sync] — refresh the File's cachedSize from the server
//     (Tgetattr on .L, Tstat on .u); backs Seek(SeekEnd) after
//     concurrent writes.
//
// ## Path Manipulation
//
//   - [Conn.Rename] — rename across directories. (.L uses Trenameat;
//     .u returns [ErrNotSupported].)
//   - [Conn.Remove] — remove file or directory (auto-detects QTDIR for
//     Tunlinkat's AT_REMOVEDIR flag on .L). (.L-only)
//   - [Conn.Link] — create a hard link. (.L-only)
//   - [Conn.Mknod] — create a device / fifo / socket node. (.L-only)
//
// ## Dialect Compatibility
//
// Methods marked .L-only return a wrapped [ErrNotSupported] on a
// 9P2000.u-negotiated Conn. Use [errors.Is] to discriminate. The
// single .u-only Raw primitive is [Raw.Tstat] — [File.Stat]
// dispatches on dialect so callers never see this asymmetry.
//
// ## Not Supported
//
// The Tauth afid handshake is not implemented. [Conn.Attach] always
// passes NoFid; authentication must be handled at the transport layer
// (TLS, SSH). See the "Authentication Scope" section above.
//
// Chmod / Chown / Truncate convenience helpers are deferred — callers
// invoke [File.Setattr] with the appropriate [proto.SetAttr].Valid
// bitmask. A future ergonomic pass may add wrappers if consumer demand
// surfaces.
//
// Twstat is intentionally unexposed on the high-level surface. .u
// callers that need path-rename or metadata-write semantics compose
// [Raw] primitives directly.
//
// # Raw Sub-Surface
//
// The [Raw] type returned by [Conn.Raw] exposes direct 9P wire
// operations with explicit fid arguments — [Raw.Read], [Raw.Write],
// [Raw.Walk], [Raw.Clunk], [Raw.Flush], [Raw.Lopen], [Raw.Lcreate],
// [Raw.Open], [Raw.Create], [Raw.Attach]. Plus [Raw.AcquireFid] and
// [Raw.ReleaseFid] integrate with the Conn's fid allocator for
// callers doing fully-explicit lifecycle management.
//
// Raw is the escape hatch for callers that need to pipeline
// T-messages manually, track fids in a parallel data structure, or
// port an existing 9P client that expects wire-level primitives. The
// high-level [File] surface handles offset tracking, fid lifecycle,
// and io.* interface conformance — use it for typical read/write
// workloads and fall through to Raw only when the high-level shape
// does not fit.
//
// # SEED-001 Resolution
//
// The v1.3.0 client API design resolved SEED-001 (see
// .planning/seeds/) as a sync-primary + async-escape shape. [File] is
// a synchronous, io.*-composable handle; [Raw] is the async/pipeline-
// friendly escape hatch. This departs from hugelgupf/p9 (ReadAt /
// WriteAt only, no io.Reader) and docker/go-p9p (raw T/R only, no
// File) — the motivation is composition with the Go standard I/O
// ecosystem. Callers that need pipelined writes (e.g. 128 KiB chunks
// issued in parallel) use [Raw] over the goroutine-safe Conn.
package client

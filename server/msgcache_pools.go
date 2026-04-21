//go:build !nocache

package server

import (
	"github.com/dotwaffle/ninep/internal/pool"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// Bounded-channel caches for hot-path request message types. See
// internal/pool for the generic primitive. Each cache is a package-global
// pool.Cache[T] instance; concurrent Get/Put is atomic (channel semantics).
//
// Cached types mirror the pre-extraction set from ninep v1.1.15 —
// v1.2.x's msgcache.go (deleted in Phase 18):
//   - Tread, Twrite (data I/O — bulk of traffic)
//   - Twalk, Tclunk (every open/close cycle)
//   - Tlopen, Tgetattr (file metadata lifecycle)
//   - Tlcreate (added Phase 13-05 for create-write-close micro-workloads)
//   - Tremove, Tmkdir, Tsetattr (file lifecycle metadata — Phase 33)
//   - Trename, Tsymlink, Tmknod (common structural metadata — Phase 33)
//
// NOT_CACHED (declined per 13-05 profile audit, 2026-04-16):
// Trenameat, Tunlinkat, Tlink, Txattrwalk, Txattrcreate, Tlock, Tgetlock — cold-path ops with
// zero allocs in the 17-bench suite. See .planning/phases/13/13-05-audit.md.
var (
	treadCache    = pool.NewCache[proto.Tread]()
	twriteCache   = pool.NewCache[proto.Twrite]()
	twalkCache    = pool.NewCache[proto.Twalk]()
	tclunkCache   = pool.NewCache[proto.Tclunk]()
	tlopenCache   = pool.NewCache[p9l.Tlopen]()
	tgetattrCache = pool.NewCache[p9l.Tgetattr]()
	tlcreateCache = pool.NewCache[p9l.Tlcreate]()
	tremoveCache  = pool.NewCache[proto.Tremove]()
	tmkdirCache   = pool.NewCache[p9l.Tmkdir]()
	tsetattrCache = pool.NewCache[p9l.Tsetattr]()
	trenameCache  = pool.NewCache[p9l.Trename]()
	tsymlinkCache = pool.NewCache[p9l.Tsymlink]()
	tmknodCache   = pool.NewCache[p9l.Tmknod]()
)

// putCachedMsg returns msg to its type-specific cache via a non-blocking
// send. No-op for types not in the cache set or when the cache is full.
// Called from dispatch.go's defer and from handleRequest's error paths
// after the handler has finished reading the request.
//
// Twrite.Data is explicitly cleared BEFORE the Put: it aliased a pooled
// bufpool buffer that is being returned to bufpool by this same defer;
// leaving Data non-nil would let the next cached-Twrite borrower observe
// a recycled bucket on any decode failure between nwname and the data
// read. The Cache[T].Get side zero-resets *m via *m = *new(T) — but
// between Put and Get, a concurrent peer could observe the stale pointer,
// so the Put-side clear is belt-and-braces.
//
// Twalk.Names is NOT cleared here: DecodeFrom rebuilds it via `make`,
// and Phase 13's TestTwalkReuseDoesNotAliasStrings locks that invariant
// into CI via unsafe.SliceData identity.
//
// Tlcreate.Name, Tmkdir.Name, Tremove.Fid etc. are scalar or Go strings
// (immutable backing store) — Get-side zero-reset covers them.
func putCachedMsg(msg proto.Message) {
	switch m := msg.(type) {
	case *proto.Tread:
		treadCache.Put(m)
	case *proto.Twrite:
		m.Data = nil
		twriteCache.Put(m)
	case *proto.Twalk:
		twalkCache.Put(m)
	case *proto.Tclunk:
		tclunkCache.Put(m)
	case *p9l.Tlopen:
		tlopenCache.Put(m)
	case *p9l.Tgetattr:
		tgetattrCache.Put(m)
	case *p9l.Tlcreate:
		tlcreateCache.Put(m)
	case *proto.Tremove:
		tremoveCache.Put(m)
	case *p9l.Tmkdir:
		tmkdirCache.Put(m)
	case *p9l.Tsetattr:
		tsetattrCache.Put(m)
	case *p9l.Trename:
		trenameCache.Put(m)
	case *p9l.Tsymlink:
		tsymlinkCache.Put(m)
	case *p9l.Tmknod:
		tmknodCache.Put(m)
	}
}

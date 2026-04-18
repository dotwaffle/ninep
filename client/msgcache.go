package client

import (
	"github.com/dotwaffle/ninep/internal/pool"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// Client-side R-message caches. Inverse of server/msgcache_pools.go's T-set:
// the server caches the T-messages it decodes most often; the client caches
// the R-messages it decodes most often.
//
// Per .planning/phases/19/19-RESEARCH.md §6 (Codec Dispatch Encore — Factory
// Tables), the hot-path R-messages are:
//
//   - Rread, Rwrite  — data I/O, bulk of traffic
//   - Rwalk, Rclunk  — every open/close cycle
//   - Rlopen, Rlcreate — file lifecycle (per open/create)
//   - Rlerror        — per-request failure path (.L)
//
// NOT cached (intentional):
//
//   - Rversion, Rattach, Rflush — low-volume (once per Conn or per attach)
//   - Rerror (.u) — dialect minority; cache hit rate too low to justify
//   - Rgetattr, Rreaddir, Rreadlink, Rstatfs, Rsetattr, etc. —
//     per the server-side 13-05 profile audit, cold paths rarely warrant
//     cache slots. Revisit if a future profile run shows otherwise.
//
// Each cache is bounded at pool.Cap (=3) slots. Concurrent Get/Put is atomic
// via channel semantics. See internal/pool/cache.go for the full contract.
var (
	rreadCache    = pool.NewCache[proto.Rread]()
	rwriteCache   = pool.NewCache[proto.Rwrite]()
	rwalkCache    = pool.NewCache[proto.Rwalk]()
	rclunkCache   = pool.NewCache[proto.Rclunk]()
	rlopenCache   = pool.NewCache[p9l.Rlopen]()
	rlcreateCache = pool.NewCache[p9l.Rlcreate]()
	rlerrorCache  = pool.NewCache[p9l.Rlerror]()
)

// getCachedRread returns a zero-reset *proto.Rread from the cache (or a
// fresh allocation on miss). Call putCachedRMsg after the caller has
// consumed the response to return the pointer to the cache.
func getCachedRread() *proto.Rread { return rreadCache.Get() }

// getCachedRwrite returns a zero-reset *proto.Rwrite from the cache.
func getCachedRwrite() *proto.Rwrite { return rwriteCache.Get() }

// getCachedRwalk returns a zero-reset *proto.Rwalk from the cache. The
// returned message's QIDs slice is nil; the decoder is expected to make()
// a fresh slice rather than append to the cached one.
func getCachedRwalk() *proto.Rwalk { return rwalkCache.Get() }

// getCachedRclunk returns a zero-reset *proto.Rclunk from the cache.
func getCachedRclunk() *proto.Rclunk { return rclunkCache.Get() }

// getCachedRlopen returns a zero-reset *p9l.Rlopen from the cache.
func getCachedRlopen() *p9l.Rlopen { return rlopenCache.Get() }

// getCachedRlcreate returns a zero-reset *p9l.Rlcreate from the cache.
func getCachedRlcreate() *p9l.Rlcreate { return rlcreateCache.Get() }

// getCachedRlerror returns a zero-reset *p9l.Rlerror from the cache.
func getCachedRlerror() *p9l.Rlerror { return rlerrorCache.Get() }

// putCachedRMsg returns msg to its type-specific cache via a non-blocking
// send. No-op for types not in the cache set (Rversion, Rattach, T-messages)
// and for a nil argument.
//
// Aliasing invariant (mirror of server/msgcache_pools.go:60):
//
//   - Rread.Data aliases a caller-owned or bufpool-owned buffer during the
//     read path. Leaving Data non-nil after Put would let the next
//     cached-Rread borrower observe a recycled bucket or stale slice
//     between the Put and the decoder's assignment. The Cache[T].Get side
//     zero-resets *m via *m = *new(T), but a concurrent peer could observe
//     the stale pointer between Put and Get, so the Put-side clear is
//     belt-and-braces.
//
//   - Rwalk.QIDs aliases a decoder-allocated slice. Decoders rebuild the
//     slice via make() rather than append to the cached one (see the
//     server-side Twalk.Names invariant enforced by
//     TestTwalkReuseDoesNotAliasStrings in Phase 13), so nil-ing on Put is
//     belt-and-braces here too.
//
// Rlopen/Rlcreate/Rlerror have no slice fields — their Get-side zero-reset
// is sufficient.
func putCachedRMsg(msg proto.Message) {
	if msg == nil {
		return
	}
	switch m := msg.(type) {
	case *proto.Rread:
		m.Data = nil
		rreadCache.Put(m)
	case *proto.Rwrite:
		rwriteCache.Put(m)
	case *proto.Rwalk:
		m.QIDs = nil
		rwalkCache.Put(m)
	case *proto.Rclunk:
		rclunkCache.Put(m)
	case *p9l.Rlopen:
		rlopenCache.Put(m)
	case *p9l.Rlcreate:
		rlcreateCache.Put(m)
	case *p9l.Rlerror:
		rlerrorCache.Put(m)
	default:
		// Unknown type — drop to GC. Rversion/Rattach/Rflush and any
		// T-messages intentionally land here (low-volume or wrong side).
	}
}

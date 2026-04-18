// Package pool provides a generic bounded-channel cache for reusable message
// structs on the 9P server and client hot paths. It replaces the seven
// per-type chan caches that previously lived at package scope in
// ninep/server/msgcache.go (v1.1.15 — v1.2.x).
//
// # Design
//
// Each Cache[T] holds a chan *T of capacity [Cap]. Get performs a non-blocking
// receive: on hit it zero-resets *m via *m = *new(T) and returns; on miss it
// returns new(T). Put performs a non-blocking send: on a free slot it caches
// the pointer; on a full channel the pointer is dropped to the GC.
//
// Channels give atomic Get/Put without the cross-P balancing overhead that
// sync.Pool exhibits under goroutine-per-request workloads. The v1.1.15
// measurement showed ~15% regression when sync.Pool wrapped *proto.Tread
// (see ninep §Performance); the bounded-chan design sidesteps that entirely.
//
// # Aliasing invariant (caller responsibility)
//
// If T holds slice or pointer fields that aliased external storage during
// its use (e.g., proto.Twrite.Data aliases a pooled bufpool buffer), the
// caller MUST clear those fields BEFORE calling Put. Cache.Put does not
// inspect T beyond the channel send; Cache.Get's *m = *new(T) reset covers
// scalar fields and nil-assigns slice/pointer fields, but it cannot undo a
// prior Put that left a stale external pointer visible through a concurrent
// read window.
//
// The server call site enforces this in server/msgcache_pools.go
// (m.Data = nil before twriteCache.Put(m)).
package pool

// Cap is the per-Cache channel depth. Matches the msgCacheCap = 3 constant
// from the pre-extraction server/msgcache.go:14. Three slots hold the few
// in-flight messages a typical dispatch loop has in the receive-decode-handle
// pipeline without retaining unused memory. Do NOT change without updating
// the server §Performance notes and re-running the BenchmarkWalkClunk
// comparison.
const Cap = 3

// Ordering: channel-FIFO. A Put followed by three more Puts and four Gets
// returns the first Put first, not last. The previous stack-based msgcache
// in server/msgcache.go was LIFO; the change is benign because reuse
// frequency dominates reuse locality for the cap-3 workload.

// Cache is a bounded-channel cache of *T pointers reset on Get. T is
// constrained to `any` rather than proto.Message because proto.Message's
// methods use pointer receivers: *proto.Tread satisfies proto.Message but
// proto.Tread does not. The useful-to-the-caller constraint (*T satisfies
// proto.Message) cannot be expressed as a single type parameter without the
// `[T any, PT interface{ *T; proto.Message }]` two-param pattern, which
// bloats every call site. Callers enforce the relationship at the type
// switch in server/msgcache_pools.go:putCachedMsg (and Phase 19's
// putCachedRMsg).
//
// Channel operations are atomic; Cache is safe for concurrent Get/Put from
// multiple goroutines.
//
// A zero-value Cache[T] has a nil channel: both Get and Put fall through
// their non-blocking selects' default branches, yielding a no-reuse cache
// (fresh allocation on Get, drop-to-GC on Put). This is used by the
// -tags nocache build in server/msgcache_pools_nocache.go to suppress
// reuse without changing the call-site shape.
type Cache[T any] struct {
	ch chan *T
}

// NewCache returns a Cache[T] with a buffered channel of depth Cap.
func NewCache[T any]() *Cache[T] {
	return &Cache[T]{ch: make(chan *T, Cap)}
}

// Get returns a zero-reset *T from the cache if one is available, or a
// fresh new(T) allocation otherwise. The receive is non-blocking.
//
// Zero-reset: *m = *new(T) sets every field to its zero value before
// returning, matching the pre-extraction per-type `*m = proto.Twrite{}`
// pattern. Callers that need belt-and-braces clearing of aliasing fields
// must still clear them on the Put side — see the package-level comment.
func (c *Cache[T]) Get() *T {
	select {
	case m := <-c.ch:
		*m = *new(T)
		return m
	default:
		return new(T)
	}
}

// Put returns m to the cache if a slot is free, or drops it to the GC
// otherwise. The send is non-blocking. Callers whose *T aliased external
// storage (pooled buffers, caller-owned slices) MUST clear those fields
// before calling Put so a concurrent peer that hits the cache on Get does
// not observe stale external pointers between the Get zero-reset and the
// new decode's assignment.
func (c *Cache[T]) Put(m *T) {
	select {
	case c.ch <- m:
	default:
	}
}

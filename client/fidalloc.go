package client

import (
	"sync"

	"github.com/dotwaffle/ninep/proto"
)

// fidStart is the first fid handed out. Zero is reserved — keeps is-zero
// sentinel checks unambiguous and matches hugelgupf/p9's convention.
const fidStart proto.Fid = 1

// reuseCacheSize caps the reuse slice to prevent unbounded growth. Excess
// released fids drop on the floor; the 4B-fid counter space tolerates
// discarded slots for any practical workload per 20-RESEARCH.md §5.
const reuseCacheSize = 1024

// fidAllocator hands out proto.Fid values for a single Conn. Uses a
// monotonic counter for first-time allocation and a bounded LIFO
// free-list for reuse. Once the counter reaches proto.NoFid with an
// empty reuse list, acquire returns ErrFidExhausted.
//
// Design rationale (20-RESEARCH.md §5): matches hugelgupf/p9's pool
// shape (counter + cache) rather than Phase 19's tagAllocator
// (pre-seeded chan) because the fid address space is 32-bit and cannot
// be pre-seeded without a multi-GiB allocation at construction.
//
// Ordering contract (Pitfall 6 in 20-RESEARCH.md §9): callers MUST
// release AFTER the server-side Tclunk response is received, never
// before. Releasing before the Rclunk lands opens a fid-reuse race
// where the allocator hands the same number to another goroutine that
// then issues Twalk against a server-view still bound to the prior
// user.
type fidAllocator struct {
	mu    sync.Mutex
	next  proto.Fid   // next fresh fid; monotonic
	reuse []proto.Fid // released fids awaiting reuse; bounded at reuseCacheSize
}

// newFidAllocator returns a fresh allocator. next starts at fidStart (1)
// and the reuse slice is preallocated with cap = reuseCacheSize so that
// release never grows the slice past the cap and the steady-state path
// costs zero allocations.
func newFidAllocator() *fidAllocator {
	return &fidAllocator{
		next:  fidStart,
		reuse: make([]proto.Fid, 0, reuseCacheSize),
	}
}

// acquire returns a fresh or reused fid. Returns ErrFidExhausted when
// the counter has walked past proto.NoFid with the reuse cache empty.
// Threadsafe.
func (fa *fidAllocator) acquire() (proto.Fid, error) {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	if n := len(fa.reuse); n > 0 {
		f := fa.reuse[n-1]
		fa.reuse = fa.reuse[:n-1]
		return f, nil
	}
	if fa.next >= proto.NoFid {
		return 0, ErrFidExhausted
	}
	f := fa.next
	fa.next++
	return f, nil
}

// release returns fid to the reuse cache, capped at reuseCacheSize. Past
// cap, the fid is dropped — harmless under the 4B-fid counter budget.
// Threadsafe.
func (fa *fidAllocator) release(fid proto.Fid) {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	if len(fa.reuse) < cap(fa.reuse) {
		fa.reuse = append(fa.reuse, fid)
	}
}

// len returns the current reuse-cache depth. Test hook only — not part
// of the public API and not used on any hot path.
func (fa *fidAllocator) len() int {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	return len(fa.reuse)
}

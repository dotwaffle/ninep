package client

import (
	"context"

	"github.com/dotwaffle/ninep/proto"
)

// tagAllocator hands out proto.Tag values from a bounded free-list.
//
// Design (locked in 19-CONTEXT.md D-01..D-03):
//   - Free-list chan proto.Tag, capacity = maxInflight.
//   - Seeded at construction with tags [1..maxInflight]. Tag 0 is excluded
//     by convention (matches server/bench_test.go and Linux v9fs client;
//     see 19-RESEARCH.md Pitfall 6). NoTag (0xFFFF) is reserved for Tversion
//     and likewise excluded — the WithMaxInflight clamp to 65534 ensures the
//     seed loop can never reach it.
//   - Natural back-pressure (D-02): when saturated, acquire blocks until a
//     tag is released. No separate semaphore.
//   - Not an atomic-counter + bitmap (D-03): scan cost is visible under
//     contention; chan Get/Put are O(1) and serialize at the bottleneck
//     already imposed by the goroutine scheduler.
type tagAllocator struct {
	free chan proto.Tag
}

// newTagAllocator seeds the free-list with tags [1..n]. The caller is
// responsible for clamping n to [1..maxMaxInflight] via WithMaxInflight
// before construction.
func newTagAllocator(n int) *tagAllocator {
	ta := &tagAllocator{free: make(chan proto.Tag, n)}
	for i := 1; i <= n; i++ {
		ta.free <- proto.Tag(i)
	}
	return ta
}

// acquire returns a free tag, blocking until one is available, ctx is
// cancelled, or stop is closed. The returned tag is non-zero and not
// proto.NoTag; callers must release it exactly once.
//
// Ordering of the select branches is not load-bearing — Go's select is
// randomized when multiple cases are ready, which is the correct behavior:
// a pending shutdown wins against a healthy pool only eventually, but the
// pool drains fairly in the common case.
func (ta *tagAllocator) acquire(ctx context.Context, stop <-chan struct{}) (proto.Tag, error) {
	select {
	case t := <-ta.free:
		return t, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-stop:
		return 0, ErrClosed
	}
}

// release returns tag to the free-list. Non-blocking: the channel has
// capacity = maxInflight and only the goroutine that acquired `tag` may
// call release, so the send never blocks in correct usage.
//
// A full channel here implies a double-release — a caller bug that -race
// stress tests (TestTagAllocator_Stress_TagReuse) surface. Dropping the
// extra release is the safest local response; logging would require a
// logger dependency that tags.go intentionally avoids.
func (ta *tagAllocator) release(tag proto.Tag) {
	select {
	case ta.free <- tag:
	default:
		// Unreachable under correct usage; drop.
	}
}

package client

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dotwaffle/ninep/proto"
)

// TestFidAllocator_AcquireStartsAtOne verifies a fresh allocator hands out
// fid 1 on first acquire, fid 2 on second. Zero is reserved (fidStart = 1).
func TestFidAllocator_AcquireStartsAtOne(t *testing.T) {
	t.Parallel()
	fa := newFidAllocator()

	f1, err := fa.acquire()
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if f1 != 1 {
		t.Fatalf("first fid = %d, want 1 (fidStart)", f1)
	}

	f2, err := fa.acquire()
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if f2 != 2 {
		t.Fatalf("second fid = %d, want 2", f2)
	}
}

// TestFidAllocator_ReleaseReused verifies acquire→release(fid=1) leaves the
// next acquire returning 1 via the LIFO reuse cache.
func TestFidAllocator_ReleaseReused(t *testing.T) {
	t.Parallel()
	fa := newFidAllocator()

	f1, err := fa.acquire()
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	fa.release(f1)

	f2, err := fa.acquire()
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	if f2 != f1 {
		t.Fatalf("re-acquire fid = %d, want %d (LIFO reuse)", f2, f1)
	}
}

// TestFidAllocator_ReuseCapBounded verifies 2000 releases leave len() <=
// reuseCacheSize (1024) with no panic and no underlying slice cap growth
// past reuseCacheSize.
func TestFidAllocator_ReuseCapBounded(t *testing.T) {
	t.Parallel()
	fa := newFidAllocator()

	// Acquire a sentinel fid to prove the allocator is alive after the
	// release flood.
	first, err := fa.acquire()
	if err != nil {
		t.Fatalf("acquire sentinel: %v", err)
	}

	// Release 2000 synthetic fids. Excess beyond reuseCacheSize must drop.
	for i := range 2000 {
		fa.release(proto.Fid(1000 + i))
	}

	if got := fa.len(); got > reuseCacheSize {
		t.Fatalf("len() = %d, want <= %d", got, reuseCacheSize)
	}
	if got := fa.len(); got != reuseCacheSize {
		t.Fatalf("len() = %d, want exactly %d after 2000-release flood", got, reuseCacheSize)
	}

	// Underlying slice cap must not have grown past reuseCacheSize: the
	// implementation pre-sizes cap = reuseCacheSize and only appends when
	// len < cap, so cap growth would indicate a bounds bug.
	if c := cap(fa.reuse); c != reuseCacheSize {
		t.Fatalf("cap(reuse) = %d, want %d (implementation must not grow past cap)", c, reuseCacheSize)
	}

	_ = first
}

// TestFidAllocator_Exhaustion verifies that once next reaches proto.NoFid
// with an empty reuse cache, acquire returns (0, ErrFidExhausted). Test
// hook: directly set the next field (same package, so no export_test needed).
func TestFidAllocator_Exhaustion(t *testing.T) {
	t.Parallel()
	fa := newFidAllocator()
	fa.next = proto.NoFid // force exhaustion boundary

	got, err := fa.acquire()
	if !errors.Is(err, ErrFidExhausted) {
		t.Fatalf("acquire at NoFid err = %v, want ErrFidExhausted", err)
	}
	if got != 0 {
		t.Fatalf("acquire at NoFid fid = %d, want 0", got)
	}
}

// TestFidAllocator_ExhaustionPrefersReuse verifies that even with next at
// NoFid, a non-empty reuse cache satisfies acquire. The exhaustion guard
// triggers only when BOTH reuse is empty AND next >= NoFid.
func TestFidAllocator_ExhaustionPrefersReuse(t *testing.T) {
	t.Parallel()
	fa := newFidAllocator()
	fa.next = proto.NoFid
	fa.reuse = append(fa.reuse, proto.Fid(42))

	got, err := fa.acquire()
	if err != nil {
		t.Fatalf("acquire with reuse at NoFid: err = %v, want nil", err)
	}
	if got != 42 {
		t.Fatalf("acquire with reuse at NoFid: fid = %d, want 42", got)
	}

	// Next acquire must now fail because reuse is empty and next >= NoFid.
	_, err = fa.acquire()
	if !errors.Is(err, ErrFidExhausted) {
		t.Fatalf("acquire after reuse drain at NoFid: err = %v, want ErrFidExhausted", err)
	}
}

// TestFidAllocator_Concurrent verifies 100 goroutines × 50 acquire-release
// cycles produce no duplicate outstanding fids and no races under -race.
// Uses sync.Map to track currently-owned fids; any double-ownership is a
// bug.
func TestFidAllocator_Concurrent(t *testing.T) {
	t.Parallel()
	const (
		workers    = 100
		iterations = 50
	)
	fa := newFidAllocator()

	var owned sync.Map // proto.Fid -> struct{}
	var dupes atomic.Int64

	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for range iterations {
				fid, err := fa.acquire()
				if err != nil {
					t.Errorf("acquire: %v", err)
					return
				}
				if _, loaded := owned.LoadOrStore(fid, struct{}{}); loaded {
					dupes.Add(1)
					t.Errorf("fid %d handed out while still owned", fid)
					return
				}
				owned.Delete(fid)
				fa.release(fid)
			}
		}()
	}
	wg.Wait()

	if d := dupes.Load(); d != 0 {
		t.Fatalf("observed %d double-ownership events", d)
	}
}

// TestFidAllocator_LeakStress_1000Cycles verifies 1000 acquire→release pairs
// leave len() bounded at <= reuseCacheSize. Steady-state reuse means the
// counter does not grow past a small bound either.
//
// NOT t.Parallel(): testing.AllocsPerRun panics if invoked under a parallel
// test (see src/testing/allocs.go). The alloc probe is load-bearing here
// so we sacrifice parallelism rather than coverage.
func TestFidAllocator_LeakStress_1000Cycles(t *testing.T) {
	fa := newFidAllocator()

	for i := range 1000 {
		fid, err := fa.acquire()
		if err != nil {
			t.Fatalf("acquire iteration %d: %v", i, err)
		}
		fa.release(fid)
	}

	if got := fa.len(); got > reuseCacheSize {
		t.Fatalf("len() after 1000 cycles = %d, want <= %d", got, reuseCacheSize)
	}
	// Single acquire→release pair at a time means reuse cache holds one
	// fid at rest. This asserts the happy-path steady state.
	if got := fa.len(); got != 1 {
		t.Fatalf("len() after 1000 cycles = %d, want 1 (single-outstanding steady state)", got)
	}

	// Steady-state allocations: after warm-up, acquire+release must not
	// allocate. Uses testing.AllocsPerRun, which runs the closure several
	// times and averages.
	allocs := testing.AllocsPerRun(100, func() {
		fid, err := fa.acquire()
		if err != nil {
			t.Fatalf("acquire in alloc probe: %v", err)
		}
		fa.release(fid)
	})
	if allocs != 0 {
		t.Fatalf("acquire+release allocs/op = %v, want 0 (steady-state zero-alloc)", allocs)
	}
}

package pool

import (
	"sync"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
)

// TestCache_GetEmpty asserts that Get on a freshly-constructed Cache returns
// a fresh non-nil *T each call: no caching occurs without a prior Put, and
// the second Get returns a different pointer than the first.
func TestCache_GetEmpty(t *testing.T) {
	t.Parallel()
	c := NewCache[proto.Tread]()

	m1 := c.Get()
	if m1 == nil {
		t.Fatal("Get on empty cache returned nil")
	}
	if m1.Fid != 0 || m1.Offset != 0 || m1.Count != 0 {
		t.Errorf("Get on empty cache returned non-zero struct: %+v", *m1)
	}

	m2 := c.Get()
	if m2 == nil {
		t.Fatal("second Get on empty cache returned nil")
	}
	if m1 == m2 {
		t.Error("Get on empty cache returned same pointer twice; expected distinct allocations")
	}
}

// TestCache_PutThenGetReuses asserts the core reuse invariant: Put a pointer,
// Get it back with fields zero-reset; the returned pointer is bit-identical
// to the one that was Put (the cache held it, not a fresh allocation).
func TestCache_PutThenGetReuses(t *testing.T) {
	t.Parallel()
	c := NewCache[proto.Tread]()

	m1 := c.Get()
	m1.Fid = 42
	m1.Offset = 1000
	m1.Count = 4096
	c.Put(m1)

	m2 := c.Get()
	if m2 != m1 {
		t.Errorf("expected Get to return cached pointer %p, got %p", m1, m2)
	}
	if m2.Fid != 0 || m2.Offset != 0 || m2.Count != 0 {
		t.Errorf("Get did not zero-reset cached struct: %+v", *m2)
	}
}

// TestCache_PutOnFullDropsToGC asserts that putting more pointers than Cap
// silently drops overflow — the cache channel length reaches Cap and stays
// there. The fourth pointer must be dropped (not block, not panic).
func TestCache_PutOnFullDropsToGC(t *testing.T) {
	t.Parallel()
	c := NewCache[proto.Tread]()

	// Put Cap+1 distinct pointers. The last one must drop silently.
	ptrs := make([]*proto.Tread, Cap+1)
	for i := range ptrs {
		ptrs[i] = &proto.Tread{Fid: proto.Fid(i)}
		c.Put(ptrs[i])
	}

	if got := len(c.ch); got != Cap {
		t.Errorf("after %d Puts, len(cache.ch) = %d, want %d", Cap+1, got, Cap)
	}

	// Drain the cache and verify we get Cap pointers back (one of the
	// original four was dropped). We don't assert which one — FIFO channel
	// order just means the first Cap-sized Puts stayed; the last was lost.
	seen := make(map[*proto.Tread]bool)
	for range Cap {
		m := c.Get()
		seen[m] = true
	}
	if len(seen) != Cap {
		t.Errorf("drained %d distinct pointers, want %d", len(seen), Cap)
	}
}

// TestCache_ConcurrentGetPut stresses Get/Put under -race with many
// goroutines. The cache must not deadlock, panic, or report races.
// Each goroutine does a Get/Put cycle repeatedly; any bug in the
// non-blocking select discipline surfaces here.
func TestCache_ConcurrentGetPut(t *testing.T) {
	t.Parallel()
	c := NewCache[proto.Twalk]()

	const goroutines = 200
	const iters = 1000

	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		wg.Add(goroutines)
		for g := range goroutines {
			go func(id int) {
				defer wg.Done()
				for i := range iters {
					m := c.Get()
					m.Fid = proto.Fid(id)
					m.NewFid = proto.Fid(i)
					c.Put(m)
				}
			}(g)
		}
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent Get/Put deadlocked or ran too slow (>10s)")
	}
}

// TestCache_CapConstant locks the D-05 invariant into CI: Cap == 3. Changing
// this constant requires re-running the BenchmarkWalkClunk comparison per
// the package doc; this test ensures a silent regression (Cap=1 or Cap=7)
// cannot sneak in without an explicit update here.
func TestCache_CapConstant(t *testing.T) {
	t.Parallel()
	if Cap != 3 {
		t.Fatalf("Cap = %d, want 3 (D-05 invariant)", Cap)
	}

	// Channel capacity must match the Cap constant. If NewCache ever
	// grows a different depth this test catches it.
	c := NewCache[proto.Tread]()
	if got := cap(c.ch); got != Cap {
		t.Errorf("NewCache channel cap = %d, want %d", got, Cap)
	}
}

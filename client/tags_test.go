package client

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
)

func TestTagAllocator_Seed(t *testing.T) {
	t.Parallel()
	const n = 8
	ta := newTagAllocator(n)
	ctx := context.Background()
	stop := make(chan struct{})

	seen := make(map[proto.Tag]bool, n)
	for i := 0; i < n; i++ {
		tag, err := ta.acquire(ctx, stop)
		if err != nil {
			t.Fatalf("acquire(%d) err = %v", i, err)
		}
		if tag == proto.NoTag {
			t.Fatalf("NoTag was handed out")
		}
		if tag == 0 {
			t.Fatalf("tag 0 was handed out (reserved by convention)")
		}
		if seen[tag] {
			t.Fatalf("tag %d handed out twice", tag)
		}
		seen[tag] = true
	}
	for i := 1; i <= n; i++ {
		if !seen[proto.Tag(i)] {
			t.Fatalf("tag %d missing from seed", i)
		}
	}
}

func TestTagAllocator_AcquireRelease(t *testing.T) {
	t.Parallel()
	ta := newTagAllocator(2)
	ctx := context.Background()
	stop := make(chan struct{})

	t1, err := ta.acquire(ctx, stop)
	if err != nil {
		t.Fatalf("acquire t1: %v", err)
	}
	t2, err := ta.acquire(ctx, stop)
	if err != nil {
		t.Fatalf("acquire t2: %v", err)
	}
	if t1 == t2 {
		t.Fatalf("duplicate tag handed out: %d", t1)
	}

	ta.release(t1)
	t3, err := ta.acquire(ctx, stop)
	if err != nil {
		t.Fatalf("acquire t3: %v", err)
	}
	if t3 != t1 {
		// Not strictly required by the API, but shape must not panic and
		// the released tag should be in the pool.
		t.Logf("released t1=%d, re-acquired t3=%d (acceptable: pool may reorder)", t1, t3)
	}
}

func TestTagAllocator_BlocksOnSaturation(t *testing.T) {
	t.Parallel()
	ta := newTagAllocator(2)
	stop := make(chan struct{})
	defer close(stop)

	// Drain both tags.
	t1, err := ta.acquire(context.Background(), stop)
	if err != nil {
		t.Fatalf("acquire t1: %v", err)
	}
	_, err = ta.acquire(context.Background(), stop)
	if err != nil {
		t.Fatalf("acquire t2: %v", err)
	}

	// Third acquire must block.
	done := make(chan proto.Tag, 1)
	errCh := make(chan error, 1)
	go func() {
		tag, err := ta.acquire(context.Background(), stop)
		if err != nil {
			errCh <- err
			return
		}
		done <- tag
	}()

	select {
	case <-done:
		t.Fatalf("third acquire returned without saturation block")
	case err := <-errCh:
		t.Fatalf("third acquire returned error prematurely: %v", err)
	case <-time.After(50 * time.Millisecond):
		// Good — blocked as expected.
	}

	// Release one; goroutine must unblock.
	ta.release(t1)
	select {
	case tag := <-done:
		if tag != t1 {
			t.Logf("got tag %d from pool (released %d; reorder acceptable)", tag, t1)
		}
	case err := <-errCh:
		t.Fatalf("goroutine got err after release: %v", err)
	case <-time.After(1 * time.Second):
		t.Fatalf("goroutine did not unblock after release")
	}
}

func TestTagAllocator_CtxCancel(t *testing.T) {
	t.Parallel()
	ta := newTagAllocator(1)
	stop := make(chan struct{})

	// Saturate.
	if _, err := ta.acquire(context.Background(), stop); err != nil {
		t.Fatalf("saturate: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ta.acquire(ctx, stop)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("acquire with cancelled ctx err = %v, want context.Canceled", err)
	}
}

func TestTagAllocator_CloseCh(t *testing.T) {
	t.Parallel()
	ta := newTagAllocator(1)
	stop := make(chan struct{})

	// Saturate.
	if _, err := ta.acquire(context.Background(), stop); err != nil {
		t.Fatalf("saturate: %v", err)
	}

	// Close stop from another goroutine; pending acquire returns ErrClosed.
	errCh := make(chan error, 1)
	go func() {
		_, err := ta.acquire(context.Background(), stop)
		errCh <- err
	}()

	time.Sleep(10 * time.Millisecond)
	close(stop)

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("acquire after close err = %v, want ErrClosed", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("acquire did not return after stop close")
	}
}

func TestTagAllocator_ReleaseAfterAllocation(t *testing.T) {
	t.Parallel()
	ta := newTagAllocator(3)
	ctx := context.Background()
	stop := make(chan struct{})

	tag, err := ta.acquire(ctx, stop)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	ta.release(tag)

	// Another acquire must succeed without blocking.
	tag2, err := ta.acquire(ctx, stop)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	// We don't require FIFO order; shape must not panic.
	_ = tag2
}

func TestTagAllocator_NoTagExcluded(t *testing.T) {
	t.Parallel()
	ta := newTagAllocator(maxMaxInflight)
	ctx := context.Background()
	stop := make(chan struct{})

	for i := 0; i < maxMaxInflight; i++ {
		tag, err := ta.acquire(ctx, stop)
		if err != nil {
			t.Fatalf("acquire(%d): %v", i, err)
		}
		if tag == proto.NoTag {
			t.Fatalf("NoTag (0xFFFF) was handed out at iteration %d", i)
		}
		if tag == 0 {
			t.Fatalf("tag 0 was handed out at iteration %d", i)
		}
	}
}

func TestTagAllocator_Stress_TagReuse(t *testing.T) {
	t.Parallel()
	const (
		maxInflight = 32
		workers     = 8
		iterations  = 1000
	)
	ta := newTagAllocator(maxInflight)
	ctx := context.Background()
	stop := make(chan struct{})

	var owners sync.Map // proto.Tag -> bool (true = currently owned)

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				tag, err := ta.acquire(ctx, stop)
				if err != nil {
					t.Errorf("acquire: %v", err)
					return
				}
				// Double-hand-out check.
				if prev, loaded := owners.LoadOrStore(tag, true); loaded {
					if prev.(bool) {
						t.Errorf("tag %d handed out while still owned", tag)
						return
					}
					owners.Store(tag, true)
				}
				// Simulated work.
				owners.Delete(tag)
				ta.release(tag)
			}
		}()
	}
	wg.Wait()
}

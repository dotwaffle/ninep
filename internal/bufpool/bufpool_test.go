package bufpool

import (
	"bytes"
	"testing"
)

func TestGetBuf_ReturnsReset(t *testing.T) {
	t.Parallel()
	b := GetBuf()
	b.WriteString("dirty")
	PutBuf(b)

	b2 := GetBuf()
	defer PutBuf(b2)
	if b2.Len() != 0 {
		t.Errorf("GetBuf returned non-empty buffer: len=%d", b2.Len())
	}
}

func TestGetBuf_PreGrown(t *testing.T) {
	t.Parallel()
	b := GetBuf()
	defer PutBuf(b)
	if b.Cap() < PoolMaxBufSize {
		t.Errorf("GetBuf buffer not pre-grown: cap=%d want >= %d", b.Cap(), PoolMaxBufSize)
	}
}

func TestPutBuf_DropsOversized(t *testing.T) {
	t.Parallel()
	// Create an oversized buffer directly (bypass pool New) and Put it.
	// The cap-guard should drop it. We cannot directly observe the pool's
	// internal state, but we can verify the function does not panic and
	// executes the drop path via code inspection -- and by asserting that
	// calling PutBuf on an oversized buffer followed by many GetBuf calls
	// does not surface a buffer with that specific cap.
	oversized := bytes.NewBuffer(make([]byte, 0, PoolMaxBufSize*2))
	PutBuf(oversized) // must not panic, must drop

	// Drain some pool entries -- if the oversized buffer leaked into the
	// pool we might see it; this is probabilistic, so we just verify
	// the drop path does not panic.
	for range 10 {
		b := GetBuf()
		if b.Cap() > PoolMaxBufSize {
			t.Errorf("oversized buffer leaked into pool: cap=%d", b.Cap())
		}
		PutBuf(b)
	}
}

func TestPutBuf_RetainsInRange(t *testing.T) {
	t.Parallel()
	// In-range buffer must be accepted by PutBuf and the next GetBuf
	// must return a zero-length buffer with cap preserved. Pool is
	// non-deterministic so we cannot assert pointer identity; we assert
	// the observable contract: len==0 and cap >= PoolMaxBufSize.
	b := GetBuf()
	b.WriteString("some data")
	PutBuf(b)

	b2 := GetBuf()
	defer PutBuf(b2)
	if b2.Len() != 0 {
		t.Errorf("GetBuf after PutBuf returned non-empty buffer: len=%d", b2.Len())
	}
	if b2.Cap() < PoolMaxBufSize {
		t.Errorf("cap not preserved across Put/Get: cap=%d want >= %d", b2.Cap(), PoolMaxBufSize)
	}
}

func TestGetPutCycle_ZeroAllocs(t *testing.T) {
	// Warm the pool -- AllocsPerRun skews on cold-pool first call.
	for range 100 {
		b := GetBuf()
		PutBuf(b)
	}

	allocs := testing.AllocsPerRun(1000, func() {
		b := GetBuf()
		PutBuf(b)
	})

	if allocs != 0 {
		t.Errorf("GetBuf+PutBuf allocs/op: got %v, want 0", allocs)
	}
}

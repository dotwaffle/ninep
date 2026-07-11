package proto

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
)

func TestReadString_Correctness(t *testing.T) {
	t.Parallel()
	cases := []string{
		"",
		"short",
		"9P2000.L",
		strings.Repeat("a", 1024),
		strings.Repeat("b", 65535),
	}
	for _, want := range cases {
		var buf bytes.Buffer
		if err := WriteString(&buf, want); err != nil {
			t.Fatalf("WriteString(%d bytes): %v", len(want), err)
		}
		got, err := ReadString(&buf)
		if err != nil {
			t.Fatalf("ReadString: %v", err)
		}
		if got != want {
			clip := func(s string) string {
				if len(s) > 20 {
					return s[:20] + "..."
				}
				return s
			}
			t.Errorf("roundtrip len=%d: got %q want %q", len(want), clip(got), clip(want))
		}
	}
}

func TestReadData(t *testing.T) {
	// No t.Parallel(): the reject-path subtest measures allocated bytes
	// via runtime.MemStats and needs a quiet heap.
	t.Run("zero count", func(t *testing.T) {
		got, err := ReadData(bytes.NewReader(nil), 0)
		if err != nil {
			t.Fatalf("ReadData(0): %v", err)
		}
		if got == nil || len(got) != 0 {
			t.Errorf("ReadData(0) = %v, want non-nil empty slice", got)
		}
	})

	t.Run("exact fit", func(t *testing.T) {
		want := []byte{1, 2, 3, 4}
		got, err := ReadData(bytes.NewReader(want), 4)
		if err != nil {
			t.Fatalf("ReadData: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("ReadData = %v, want %v", got, want)
		}
	})

	t.Run("count exceeds bytes.Reader remainder", func(t *testing.T) {
		// The count must be rejected before the allocation happens; assert
		// no per-call allocation grows with the claimed count.
		r := bytes.NewReader([]byte{1, 2, 3})
		if _, err := ReadData(r, MaxDataSize); err == nil {
			t.Fatal("expected error for count exceeding remaining bytes")
		}
		// Alloc counts cannot tell a 16 MiB buffer from an error wrap, so
		// measure allocated bytes: 10 rejected calls must stay far below a
		// single MaxDataSize buffer.
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for range 10 {
			if _, err := r.Seek(0, 0); err != nil {
				t.Fatalf("seek: %v", err)
			}
			_, _ = ReadData(r, MaxDataSize)
		}
		runtime.ReadMemStats(&after)
		if delta := after.TotalAlloc - before.TotalAlloc; delta > 1<<20 {
			t.Errorf("ReadData reject path allocated %d bytes over 10 calls; count is not being checked before allocation", delta)
		}
	})

	t.Run("short generic reader", func(t *testing.T) {
		if _, err := ReadData(strings.NewReader("ab"), 4); err == nil {
			t.Fatal("expected error for short generic reader")
		}
	})
}

func TestReadString_PooledAllocs(t *testing.T) {
	// Pre-encode a typical string once so benchmark data is static.
	var encoded bytes.Buffer
	encoded.Grow(64)
	if err := WriteString(&encoded, "9P2000.L"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	data := encoded.Bytes()

	// Reuse a single bytes.Reader across iterations (Reset via Seek) so we
	// measure only ReadString's own allocations, not bytes.NewReader's.
	r := bytes.NewReader(data)

	// Warm the pool -- AllocsPerRun skews on cold-pool first call.
	for range 10 {
		if _, err := r.Seek(0, 0); err != nil {
			t.Fatalf("seek: %v", err)
		}
		_, _ = ReadString(r)
	}

	allocs := testing.AllocsPerRun(1000, func() {
		if _, err := r.Seek(0, 0); err != nil {
			t.Fatalf("seek: %v", err)
		}
		_, _ = ReadString(r)
	})
	// Target: 2 allocs per call.
	//   1. string(*scratch) -- unavoidable (strings are immutable in Go).
	//   2. ReadUint16 escapes its 2-byte stack buffer to the heap because
	//      io.Reader is an interface (escape analysis forces heap). Out of
	//      scope for this plan; requires refactoring decode helpers to take
	//      *bytes.Reader or equivalent concrete type.
	// Pre-pool baseline was 3 (length-buf escape + make + string).
	if allocs > 2 {
		t.Errorf("ReadString allocs/op: got %v, want <= 2", allocs)
	}
}

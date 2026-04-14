package proto

import (
	"bytes"
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

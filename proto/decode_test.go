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

	// Warm the pool -- AllocsPerRun skews on cold-pool first call.
	for range 10 {
		r := bytes.NewReader(data)
		_, _ = ReadString(r)
	}

	allocs := testing.AllocsPerRun(1000, func() {
		r := bytes.NewReader(data)
		_, _ = ReadString(r)
	})
	// Target: 1 alloc per call (the unavoidable string() copy).
	// Baseline was 2 (make([]byte, length) + string(data)).
	if allocs > 2 {
		t.Errorf("ReadString allocs/op: got %v, want <= 2", allocs)
	}
}

package client

import "testing"

// TestChunkLen verifies chunkLen clamps to m without truncating n's
// high bits first.
func TestChunkLen(t *testing.T) {
	tests := []struct {
		name string
		n    int
		m    uint32
		want uint32
	}{
		{"n below m", 10, 100, 10},
		{"n equal m", 100, 100, 100},
		{"n above m", 200, 100, 100},
		{"n zero", 0, 100, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := chunkLen(tc.n, tc.m); got != tc.want {
				t.Errorf("chunkLen(%d, %d) = %d, want %d", tc.n, tc.m, got, tc.want)
			}
		})
	}

	// n far exceeds uint32's range: naive uint32(n) would truncate to a
	// value that could land anywhere, possibly even below m. chunkLen
	// must still clamp to m. Only reachable when int is 64 bits (this
	// repo cross-builds to 386/arm, where int itself cannot hold a
	// value past uint32's range in the first place).
	//
	// wordBits and the uint64->int conversion below both operate on
	// variables, never on an untyped constant literal past int32's
	// range -- keeps this file compiling on 32-bit GOARCH targets,
	// where such a literal would be a compile-time overflow error
	// regardless of the runtime skip.
	wordBits := 32 << (^uint(0) >> 63)
	if wordBits <= 32 {
		t.Skip("int is 32 bits on this platform; skipping the >uint32-range case")
	}
	var base uint64 = 1<<32 + 1<<20
	huge := int(base)
	const m = uint32(65536)
	if got := chunkLen(huge, m); got != m {
		t.Errorf("chunkLen(%d, %d) = %d, want %d", huge, m, got, m)
	}
}

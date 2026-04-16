package server

import (
	"bytes"
	"testing"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// TestRecvPath_PoolReusesBuffer asserts (via indirect Decode simulation) that
// the decode-side allocation profile is within the post-08-04 budget. Full
// recv-path pool coverage lives in BenchmarkReadDecode; this test is the
// in-process guard that catches regressions without requiring benchstat.
func TestRecvPath_PoolReusesBuffer(t *testing.T) {
	// Encode a Tversion message once (smallest message type with a string
	// field exercising ReadString's pooled scratch path).
	var encoded bytes.Buffer
	if err := p9l.Encode(&encoded, 1, &proto.Tversion{Msize: 65536, Version: "9P2000.L"}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	raw := encoded.Bytes()

	// Warm pools.
	for range 10 {
		_, _, _ = p9l.Decode(bytes.NewReader(raw))
	}

	allocs := testing.AllocsPerRun(100, func() {
		_, _, _ = p9l.Decode(bytes.NewReader(raw))
	})

	// Phase 7 baseline BenchmarkReadDecode was 11 allocs/op. Task 2 pooled
	// ReadString (saves ~1 per string field). Task 3 pools the recv-path
	// msg-body buffer (saves 1 in BenchmarkReadDecode via a separate path;
	// this test measures only Decode, so the gain is driven by ReadString).
	// Target: < 10 allocs/op for a message with a single string field.
	if allocs > 10 {
		t.Errorf("p9l.Decode allocs/op: got %v, want < 10 after 08-04 ReadString pool", allocs)
	}
}

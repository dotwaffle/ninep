package p9l_test

import (
	"bytes"
	"testing"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// TestEncode_ZeroAllocs pins the post-08-03 allocation budget for p9l.Encode.
//
// After Phase 8:
//   - Plan 08-02 removed per-field escape by giving proto.Write* a concrete
//     *bytes.Buffer fast path.
//   - Plan 08-03 pooled the body buffer itself (var body bytes.Buffer →
//     bufpool.GetBuf/defer PutBuf).
//
// Combined, the alloc budget for Encode depends only on the message's own
// encoding needs. For fixed-size payloads (Tversion, Twalk nwname=0, Tclunk)
// the budget is 0. For payloads that allocate internally (e.g., Rgetattr
// populates several fields through reflection-free paths), the residual is
// measured empirically against a small ceiling. Rread_{4k,64k} are excluded
// — the payload is caller-owned, not allocated in Encode.
func TestEncode_ZeroAllocs(t *testing.T) {
	cases := []struct {
		name     string
		msg      proto.Message
		maxAlloc float64
	}{
		{"Tversion", &proto.Tversion{Msize: 65536, Version: "9P2000.L"}, 0},
		{"Twalk_nwname=0", &proto.Twalk{Fid: 1, NewFid: 2}, 0},
		{"Twalk_nwname=5", &proto.Twalk{Fid: 1, NewFid: 2, Names: []string{"a", "b", "c", "d", "e"}}, 0},
		{"Tclunk", &proto.Tclunk{Fid: 1}, 0},
		{"Tread", &proto.Tread{Fid: 1, Offset: 0, Count: 4096}, 0},
		{"Rgetattr", &p9l.Rgetattr{Attr: proto.Attr{Valid: 0x3fff, QID: proto.QID{Type: proto.QTFILE, Version: 1, Path: 42}, Mode: 0o644}}, 0},
		{"Rlopen", &p9l.Rlopen{QID: proto.QID{Type: proto.QTFILE, Path: 42}, IOUnit: 4096}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sink bytes.Buffer
			sink.Grow(1024)
			// Warm the sink and the pool. The pool's first GetBuf in this
			// process returns the pre-grown New()-value buffer; subsequent
			// Get/Put cycles are zero-alloc.
			for range 10 {
				sink.Reset()
				if err := p9l.Encode(&sink, 1, tc.msg); err != nil {
					t.Fatalf("warmup encode: %v", err)
				}
			}
			allocs := testing.AllocsPerRun(1000, func() {
				sink.Reset()
				_ = p9l.Encode(&sink, 1, tc.msg)
			})
			if allocs > tc.maxAlloc {
				t.Errorf("Encode(%s) allocs/op: got %v, want <= %v", tc.name, allocs, tc.maxAlloc)
			}
		})
	}
}

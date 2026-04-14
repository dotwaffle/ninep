package p9u_test

import (
	"bytes"
	"testing"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9u"
)

// TestEncode_ZeroAllocs pins the post-08-03 allocation budget for p9u.Encode.
//
// See proto/p9l/codec_alloc_test.go for the full rationale; the u-dialect
// shares the same Encode shape, so the same budget applies. Rstat is
// included as a u-specific case with multiple string fields to stress the
// WriteString fast path.
func TestEncode_ZeroAllocs(t *testing.T) {
	cases := []struct {
		name     string
		msg      proto.Message
		maxAlloc float64
	}{
		{"Tversion", &proto.Tversion{Msize: 65536, Version: "9P2000.u"}, 0},
		{"Tclunk", &proto.Tclunk{Fid: 1}, 0},
		{"Topen", &p9u.Topen{Fid: 1, Mode: 0}, 0},
		{"Ropen", &p9u.Ropen{QID: proto.QID{Path: 42}, IOUnit: 4096}, 0},
		{"Tcreate", &p9u.Tcreate{Fid: 1, Name: "file", Perm: 0o644, Mode: 0, Extension: ""}, 0},
		{"Rstat", &p9u.Rstat{Stat: p9u.Stat{Name: "file", UID: "user", GID: "group", MUID: "user"}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sink bytes.Buffer
			sink.Grow(1024)
			for range 10 {
				sink.Reset()
				if err := p9u.Encode(&sink, 1, tc.msg); err != nil {
					t.Fatalf("warmup encode: %v", err)
				}
			}
			allocs := testing.AllocsPerRun(1000, func() {
				sink.Reset()
				_ = p9u.Encode(&sink, 1, tc.msg)
			})
			if allocs > tc.maxAlloc {
				t.Errorf("Encode(%s) allocs/op: got %v, want <= %v", tc.name, allocs, tc.maxAlloc)
			}
		})
	}
}

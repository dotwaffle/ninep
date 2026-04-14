// Package p9l benchmark suite.
//
// These benchmarks establish per-message-type encode/decode baselines for the
// 9P2000.L codec. They are intended to be captured with -count=10 in Plan 04
// and diffed against Phase 8 buffer-pool variants via benchstat.
//
// Conventions:
//   - Subtest names follow the key=value form (msg=Tversion, msg=Twalk_nwname=5),
//     which benchstat can project on to compare across CLs.
//   - Every subtest calls b.ReportAllocs so the allocs/op column is populated
//     whether or not -benchmem is passed.
//   - Every hot loop uses Go 1.26's b.Loop(), which replaces the legacy b.N
//     pattern and manages timing automatically. b.ResetTimer is deliberately
//     omitted.
//   - The bytes.Buffer used for encoding is hoisted outside the loop and
//     Reset each iteration, avoiding per-iteration allocation noise.
//   - For decode, wire frames are pre-encoded once per case; the hot loop
//     resets a bytes.Reader over the cached frame so the measured cost is
//     Decode itself, not frame construction.
package p9l

import (
	"bytes"
	"testing"

	"github.com/dotwaffle/ninep/proto"
)

// encodeCases enumerates representative 9P2000.L message types covered by the
// codec encode/decode benchmarks. The naming convention "msg=<Name>" is used
// verbatim for b.Run subtests so benchstat can project on the "msg" key.
//
// Coverage rationale:
//   - Tversion/Rversion-class small fixed payloads (header + short string)
//   - Twalk at nwname=0 and nwname=5 bracket the per-element overhead
//   - Tread/Rread at 4k and 64k bracket the typical I/O sizes (PAGE_SIZE
//     up through a common msize)
//   - Tclunk represents the minimum viable message (one uint32)
//   - Tlopen/Rlopen/Tgetattr/Rgetattr/Treaddir/Rreaddir_empty/Treadlink/
//     Rreadlink exercise 9P2000.L-specific codec paths
var encodeCases = []struct {
	name string
	msg  proto.Message
}{
	{"Tversion", &proto.Tversion{Msize: 65536, Version: "9P2000.L"}},
	{"Twalk_nwname=0", &proto.Twalk{Fid: 1, NewFid: 2}},
	{"Twalk_nwname=5", &proto.Twalk{Fid: 1, NewFid: 2, Names: []string{"a", "b", "c", "d", "e"}}},
	{"Tread", &proto.Tread{Fid: 1, Offset: 0, Count: 4096}},
	{"Rread_4k", &proto.Rread{Data: make([]byte, 4096)}},
	{"Rread_64k", &proto.Rread{Data: make([]byte, 65536)}},
	{"Tclunk", &proto.Tclunk{Fid: 1}},
	{"Tlopen", &Tlopen{Fid: 1, Flags: 0x8000}},
	{"Rlopen", &Rlopen{QID: proto.QID{Type: 0, Path: 42}, IOUnit: 4096}},
	{"Tgetattr", &Tgetattr{Fid: 1, RequestMask: 0x3fff}},
	{"Rgetattr", &Rgetattr{Attr: proto.Attr{Valid: 0x3fff, QID: proto.QID{Path: 42}, Mode: 0o644}}},
	{"Treaddir", &Treaddir{Fid: 1, Offset: 0, Count: 4096}},
	{"Rreaddir_empty", &Rreaddir{Data: nil}},
	{"Treadlink", &Treadlink{Fid: 1}},
	{"Rreadlink", &Rreadlink{Target: "/tmp/target"}},
}

// BenchmarkEncode measures encode throughput and allocations for each
// representative 9P2000.L message type. b.Loop manages iteration; SetBytes
// reports MB/s based on the fully framed wire size (header + body).
func BenchmarkEncode(b *testing.B) {
	var buf bytes.Buffer
	for _, c := range encodeCases {
		b.Run("msg="+c.name, func(b *testing.B) {
			b.ReportAllocs()

			// Dry encode to determine frame size for SetBytes.
			buf.Reset()
			if err := Encode(&buf, proto.Tag(1), c.msg); err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(buf.Len()))

			for b.Loop() {
				buf.Reset()
				if err := Encode(&buf, proto.Tag(1), c.msg); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkDecode measures decode throughput and allocations for each
// representative 9P2000.L message type. Wire frames are pre-encoded once per
// case; the hot loop resets a bytes.Reader over the cached frame and calls
// Decode.
func BenchmarkDecode(b *testing.B) {
	cases := make([]struct {
		name string
		wire []byte
	}, len(encodeCases))
	var buf bytes.Buffer
	for i, c := range encodeCases {
		buf.Reset()
		if err := Encode(&buf, proto.Tag(1), c.msg); err != nil {
			b.Fatalf("pre-encode %s: %v", c.name, err)
		}
		cases[i].name = c.name
		cases[i].wire = append([]byte(nil), buf.Bytes()...)
	}

	var r bytes.Reader
	for _, c := range cases {
		b.Run("msg="+c.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(c.wire)))

			for b.Loop() {
				r.Reset(c.wire)
				if _, _, err := Decode(&r); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

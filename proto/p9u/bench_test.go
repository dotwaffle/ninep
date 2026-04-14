package p9u

import (
	"bytes"
	"testing"

	"github.com/dotwaffle/ninep/proto"
)

// encodeCases enumerates representative 9P2000.u message types covered by the
// codec encode/decode benchmarks. The naming convention "msg=<Name>" is used
// verbatim for b.Run subtests so benchstat can project on the "msg" key.
var encodeCases = []struct {
	name string
	msg  proto.Message
}{
	{"Tversion", &proto.Tversion{Msize: 65536, Version: "9P2000.u"}},
	{"Twalk_nwname=0", &proto.Twalk{Fid: 1, NewFid: 2}},
	{"Twalk_nwname=5", &proto.Twalk{Fid: 1, NewFid: 2, Names: []string{"a", "b", "c", "d", "e"}}},
	{"Tread", &proto.Tread{Fid: 1, Offset: 0, Count: 4096}},
	{"Rread_4k", &proto.Rread{Data: make([]byte, 4096)}},
	{"Rread_64k", &proto.Rread{Data: make([]byte, 65536)}},
	{"Tclunk", &proto.Tclunk{Fid: 1}},
	{"Topen", &Topen{Fid: 1, Mode: 0}},
	{"Ropen", &Ropen{QID: proto.QID{Path: 42}, IOUnit: 4096}},
	{"Tcreate", &Tcreate{Fid: 1, Name: "file", Perm: 0o644, Mode: 0, Extension: ""}},
	{"Rcreate", &Rcreate{QID: proto.QID{Path: 43}, IOUnit: 4096}},
	{"Rerror", &Rerror{Ename: "no such file or directory", Errno: 2}},
	{"Tstat", &Tstat{Fid: 1}},
	{"Rstat", &Rstat{Stat: Stat{Name: "file", UID: "user", GID: "group", MUID: "user"}}},
}

// BenchmarkEncode measures encode throughput and allocations for each
// representative 9P2000.u message type. b.Loop manages iteration; SetBytes
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
// representative 9P2000.u message type. Wire frames are pre-encoded once per
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

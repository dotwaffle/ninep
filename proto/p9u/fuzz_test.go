package p9u_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9u"
)

// FuzzCodecRoundTrip seeds the fuzzer with valid encoded messages and then
// verifies the round-trip property: any successfully decoded message must
// re-encode to identical bytes that decode to an identical message.
func FuzzCodecRoundTrip(f *testing.F) {
	seeds := []struct {
		tag proto.Tag
		msg proto.Message
	}{
		{1, &proto.Tversion{Msize: 8192, Version: "9P2000.u"}},
		{2, &proto.Twalk{Fid: 0, NewFid: 1, Names: []string{"foo"}}},
		{3, &p9u.Rerror{Ename: "not found", Errno: proto.ENOENT}},
		{4, &p9u.Tcreate{Fid: 1, Name: "link", Perm: p9u.DMSYMLINK | 0777, Mode: 0, Extension: "/target"}},
		{5, &p9u.Rstat{Stat: p9u.Stat{
			Type: 0, Dev: 0,
			QID:       proto.QID{Type: proto.QTFILE, Version: 1, Path: 42},
			Mode:      0100644,
			Atime:     1700000000,
			Mtime:     1700000001,
			Length:    4096,
			Name:      "test.txt",
			UID:       "root",
			GID:       "root",
			MUID:      "root",
			Extension: "",
			NUid:      0,
			NGid:      0,
			NMuid:     0,
		}}},
		{6, &p9u.Topen{Fid: 10, Mode: 0}},
	}
	for _, s := range seeds {
		var buf bytes.Buffer
		if err := p9u.Encode(&buf, s.tag, s.msg); err != nil {
			f.Fatalf("seed encode: %v", err)
		}
		f.Add(buf.Bytes())
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Try to decode the fuzzed bytes.
		r := bytes.NewReader(data)
		tag, msg, err := p9u.Decode(r)
		if err != nil {
			return // Invalid input is fine -- the invariant is no panics.
		}

		// If decode succeeded, re-encode must succeed.
		var buf bytes.Buffer
		if err := p9u.Encode(&buf, tag, msg); err != nil {
			t.Fatalf("encode after successful decode failed: %v", err)
		}

		// Decode the re-encoded bytes -- must produce identical result.
		r2 := bytes.NewReader(buf.Bytes())
		tag2, msg2, err := p9u.Decode(r2)
		if err != nil {
			t.Fatalf("second decode failed: %v", err)
		}
		if tag != tag2 {
			t.Fatalf("tag mismatch: %d != %d", tag, tag2)
		}
		if !reflect.DeepEqual(msg, msg2) {
			t.Fatalf("message mismatch after round-trip:\n  got:  %+v\n  want: %+v", msg2, msg)
		}
	})
}

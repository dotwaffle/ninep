package p9l_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// FuzzCodecRoundTrip seeds the fuzzer with valid encoded messages and then
// verifies the round-trip property: any successfully decoded message must
// re-encode to identical bytes that decode to an identical message.
func FuzzCodecRoundTrip(f *testing.F) {
	seeds := []struct {
		tag proto.Tag
		msg proto.Message
	}{
		{1, &proto.Tversion{Msize: 8192, Version: "9P2000.L"}},
		{2, &proto.Twalk{Fid: 0, NewFid: 1, Names: []string{"foo"}}},
		{3, &proto.Rread{Data: []byte("hello")}},
		{4, &p9l.Rlerror{Ecode: proto.ENOENT}},
		{5, &p9l.Tgetattr{Fid: 1, RequestMask: 0x17FF}},
		{6, &p9l.Tlock{Fid: 1, LockType: 0, Flags: 0, Start: 0, Length: 100, ProcID: 1, ClientID: "h"}},
		{7, &p9l.Tmkdir{DirFid: 1, Name: "dir", Mode: 0755, GID: 0}},
		// Structurally interesting shapes: the largest fixed-field body, a
		// multi-QID variable-length response, and both Payloader messages.
		{8, &p9l.Rgetattr{Attr: proto.Attr{
			Valid: 0x17FF,
			QID:   proto.QID{Type: proto.QTDIR, Version: 2, Path: 99},
			Mode:  0o40755, UID: 1000, GID: 1000, NLink: 3,
			Size: 4096, BlkSize: 4096, Blocks: 8,
			ATimeSec: 1700000000, MTimeSec: 1700000001, CTimeSec: 1700000002,
		}}},
		{9, &proto.Rwalk{QIDs: []proto.QID{
			{Type: proto.QTDIR, Version: 1, Path: 1},
			{Type: proto.QTDIR, Version: 1, Path: 2},
			{Type: proto.QTFILE, Version: 1, Path: 3},
		}}},
		{10, &proto.Twrite{Fid: 2, Offset: 512, Data: []byte("payload")}},
		{11, &p9l.Rreaddir{Data: []byte{1, 2, 3, 4, 5, 6, 7, 8}}},
	}
	for _, s := range seeds {
		var buf bytes.Buffer
		if err := p9l.Encode(&buf, s.tag, s.msg); err != nil {
			f.Fatalf("seed encode: %v", err)
		}
		f.Add(buf.Bytes())
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Try to decode the fuzzed bytes.
		r := bytes.NewReader(data)
		tag, msg, err := p9l.Decode(r)
		if err != nil {
			return // Invalid input is fine -- the invariant is no panics.
		}

		// If decode succeeded, re-encode must succeed.
		var buf bytes.Buffer
		if err := p9l.Encode(&buf, tag, msg); err != nil {
			t.Fatalf("encode after successful decode failed: %v", err)
		}

		// Decode the re-encoded bytes -- must produce identical result.
		r2 := bytes.NewReader(buf.Bytes())
		tag2, msg2, err := p9l.Decode(r2)
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

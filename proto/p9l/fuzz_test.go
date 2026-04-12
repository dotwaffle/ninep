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

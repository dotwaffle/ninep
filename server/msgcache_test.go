//go:build !nocache

// Package server msgcache_test.go verifies the aliasing invariant for every
// message type cached in msgcache.go.
//
// Why this test exists (Phase 13, D-06 / D-07):
//
//   - D-06: lock a structural guardrail that every cached struct, once
//     returned to its bounded-chan cache and re-borrowed by a later decode,
//     does not leak prior-decode slice/string data into the new decode's
//     fields.
//   - D-07: the comment at msgcache.go:133 claims "Names is overwritten via
//     make in DecodeFrom so no zeroing needed." This test turns that claim
//     into a CI-enforced contract by asserting, via unsafe.SliceData, that
//     the Twalk.Names backing array is a fresh allocation on every decode.
//     If a future edit replaces `m.Names = make(...)` with
//     `m.Names = append(m.Names[:0], ...)` — which would alias across reuses
//     — the backing-array identity check catches the regression.
//
// unsafe.SliceData is the stdlib replacement for the deprecated
// reflect.SliceHeader (available since Go 1.20; this module requires
// Go 1.26 per go.mod). See pkg.go.dev/unsafe#SliceData.
//
// NOTE: do NOT mark subtests parallel in this file (neither at the outer
// TestCachedMsgReuseDoesNotAliasFields level nor inside the per-type
// helpers). The cache channels at msgcache.go:38-45 are package-global
// state; running these in parallel would race with each other and with
// any other server test that touches newMessage / getCached* /
// putCachedMsg. Correctness trumps speed here
// (CLAUDE.md §Testing, 13-PATTERNS.md §subtest-parallel discipline).
package server

import (
	"bytes"
	"slices"
	"testing"
	"unsafe"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// encodeBody encodes msg via p9l.Encode and returns only the message body —
// the 7-byte wire header (size[4] + type[1] + tag[2]) is stripped so the
// returned bytes feed directly into a DecodeFrom call without needing to
// reconstruct a LimitReader around the framing layer.
func encodeBody(tb testing.TB, msg proto.Message) []byte {
	tb.Helper()
	var buf bytes.Buffer
	if err := p9l.Encode(&buf, proto.Tag(0), msg); err != nil {
		tb.Fatalf("p9l.Encode(%T): %v", msg, err)
	}
	const headerSize = 4 + 1 + 2 // size + type + tag
	if buf.Len() < headerSize {
		tb.Fatalf("encoded frame shorter than header: len=%d", buf.Len())
	}
	return buf.Bytes()[headerSize:]
}

// decodeBody runs dst.DecodeFrom against a fresh bytes.Reader over wireBody.
// Any decode error is fatal; the test relies on successful decode to set up
// the aliasing check.
func decodeBody(tb testing.TB, dst proto.Message, wireBody []byte) {
	tb.Helper()
	if err := dst.DecodeFrom(bytes.NewReader(wireBody)); err != nil {
		tb.Fatalf("DecodeFrom(%T): %v", dst, err)
	}
}

// TestCachedMsgReuseDoesNotAliasFields is the table-driven guardrail for
// every currently-cached message type (see msgcache.go:38-45). Each subtest
// exercises the full decode → putCachedMsg → getCachedX → decode cycle and
// asserts that the second decode's fields reflect only the second payload.
//
// Intentionally not parallel — see the package-level comment above.
func TestCachedMsgReuseDoesNotAliasFields(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"Twalk/Names_fresh_backing_array", testTwalkAliasing},
		{"Twrite/Data_niled_on_put", testTwriteAliasing},
		{"Tread/fields_zeroed_on_get", testTreadAliasing},
		{"Tclunk/fields_zeroed_on_get", testTclunkAliasing},
		{"Tlopen/fields_zeroed_on_get", testTlopenAliasing},
		{"Tgetattr/fields_zeroed_on_get", testTgetattrAliasing},
	}
	for _, tc := range cases {
		// Subtests NOT parallel: the cache chans are package-global.
		t.Run(tc.name, tc.run)
	}
}

// TestTwalkReuseDoesNotAliasStrings is the handoff-named entry point
// (ninep-handoff 02-msg-struct-chan-cache.md names this sketch explicitly).
// It delegates to the table-driven cover so that
// `go test -run TestTwalkReuseDoesNotAliasStrings` is a valid invocation
// per the ROADMAP Phase 13 success-criterion text.
func TestTwalkReuseDoesNotAliasStrings(t *testing.T) {
	testTwalkAliasing(t)
}

// testTwalkAliasing is the critical case: Twalk.Names is a []string field
// whose claim at msgcache.go:133 is "overwritten via make in DecodeFrom."
// The backing-array pointer check (via unsafe.SliceData) turns that claim
// into a test contract — if DecodeFrom ever regresses to
// append(m.Names[:0], ...) semantics, the pointer would match and this
// subtest would fail.
func testTwalkAliasing(t *testing.T) {
	// Step 1: first decode — 3-element Names slice.
	m1 := getCachedTwalk()
	body1 := encodeBody(t, &proto.Twalk{
		Fid:    1,
		NewFid: 2,
		Names:  []string{"alpha", "beta", "gamma"},
	})
	decodeBody(t, m1, body1)
	if len(m1.Names) != 3 {
		t.Fatalf("first decode: got %d Names, want 3", len(m1.Names))
	}
	firstNamesData := unsafe.SliceData(m1.Names)

	// Step 2: return to cache, re-borrow, decode a shorter Names slice.
	// The re-borrow may hit the cached slot (same pointer) or may fall
	// through to a fresh allocation; either is valid — the invariant is
	// "no aliasing," not "must be the same pointer."
	putCachedMsg(m1)
	m2 := getCachedTwalk()
	t.Cleanup(func() { putCachedMsg(m2) })

	body2 := encodeBody(t, &proto.Twalk{
		Fid:    3,
		NewFid: 4,
		Names:  []string{"x"},
	})
	decodeBody(t, m2, body2)

	// Value check: m2 reflects only decode #2.
	if !slices.Equal(m2.Names, []string{"x"}) {
		t.Errorf("cache reuse leaked decode #1 into Names: got %v, want [x]", m2.Names)
	}
	if m2.Fid != 3 || m2.NewFid != 4 {
		t.Errorf("cache reuse leaked Fid/NewFid: got Fid=%d NewFid=%d, want Fid=3 NewFid=4", m2.Fid, m2.NewFid)
	}

	// Pointer identity check: fresh backing array (D-07 — the "make"
	// invariant locked into CI). If this ever fires, DecodeFrom has
	// regressed to append-semantics and needs either reverting or a
	// matching explicit zero-out in putCachedMsg.
	secondNamesData := unsafe.SliceData(m2.Names)
	if secondNamesData == firstNamesData {
		t.Errorf("Twalk.Names backing array aliased across cache reuse: "+
			"unsafe.SliceData(m.Names) == %p on both decodes", firstNamesData)
	}
}

// testTwriteAliasing verifies the Put-side nil-out at msgcache.go:127 —
// Twrite.Data aliases pooled bufpool memory on the live request path, so
// leaving it non-nil in the cache would let the next borrower observe a
// recycled bucket buffer on any decode error that aborts before the data
// field is overwritten. No backing-array identity check here: Data is
// intentionally aliased to bufpool memory in production; the test owns
// neither the bufpool lifecycle nor the decode-over-wire buffer, so
// pointer-identity is meaningless for this field.
func testTwriteAliasing(t *testing.T) {
	// Step 1: decode a Twrite with 16 bytes of 0xAA.
	m1 := getCachedTwrite()
	data1 := bytes.Repeat([]byte{0xAA}, 16)
	body1 := encodeBody(t, &proto.Twrite{
		Fid:    1,
		Offset: 0,
		Data:   data1,
	})
	decodeBody(t, m1, body1)
	if len(m1.Data) != 16 {
		t.Fatalf("first decode: got Data len=%d, want 16", len(m1.Data))
	}

	// Step 2: Put — must nil Data per msgcache.go:127.
	putCachedMsg(m1)
	if m1.Data != nil {
		t.Errorf("putCachedMsg(*Twrite) did not nil Data: len=%d, cap=%d", len(m1.Data), cap(m1.Data))
	}

	// Step 3: re-borrow, decode a shorter Data payload.
	m2 := getCachedTwrite()
	t.Cleanup(func() { putCachedMsg(m2) })

	data2 := bytes.Repeat([]byte{0xBB}, 4)
	body2 := encodeBody(t, &proto.Twrite{
		Fid:    2,
		Offset: 100,
		Data:   data2,
	})
	decodeBody(t, m2, body2)

	// Assertion: m2.Data contains exactly decode #2 bytes, no leak.
	if !slices.Equal(m2.Data, data2) {
		t.Errorf("cache reuse leaked decode #1 into Data: got %x, want %x", m2.Data, data2)
	}
	if m2.Fid != 2 || m2.Offset != 100 {
		t.Errorf("cache reuse leaked scalars: got Fid=%d Offset=%d, want Fid=2 Offset=100", m2.Fid, m2.Offset)
	}
}

// testTreadAliasing exercises the *m = proto.Tread{} zero-reset at
// msgcache.go:53. Tread has only scalar fields; the check is "does Get
// return a zeroed struct even when a non-zero struct was Put."
func testTreadAliasing(t *testing.T) {
	m1 := getCachedTread()
	m1.Fid, m1.Offset, m1.Count = 10, 100, 1000
	putCachedMsg(m1)

	m2 := getCachedTread()
	t.Cleanup(func() { putCachedMsg(m2) })
	if m2.Fid != 0 || m2.Offset != 0 || m2.Count != 0 {
		t.Errorf("getCachedTread did not zero struct: got %+v, want all zero", *m2)
	}
}

// testTclunkAliasing exercises the zero-reset at msgcache.go:83.
func testTclunkAliasing(t *testing.T) {
	m1 := getCachedTclunk()
	m1.Fid = 42
	putCachedMsg(m1)

	m2 := getCachedTclunk()
	t.Cleanup(func() { putCachedMsg(m2) })
	if m2.Fid != 0 {
		t.Errorf("getCachedTclunk did not zero struct: got Fid=%d, want 0", m2.Fid)
	}
}

// testTlopenAliasing exercises the zero-reset at msgcache.go:93.
func testTlopenAliasing(t *testing.T) {
	m1 := getCachedTlopen()
	m1.Fid, m1.Flags = 7, 0xDEADBEEF
	putCachedMsg(m1)

	m2 := getCachedTlopen()
	t.Cleanup(func() { putCachedMsg(m2) })
	if m2.Fid != 0 || m2.Flags != 0 {
		t.Errorf("getCachedTlopen did not zero struct: got %+v, want all zero", *m2)
	}
}

// testTgetattrAliasing exercises the zero-reset at msgcache.go:103.
func testTgetattrAliasing(t *testing.T) {
	m1 := getCachedTgetattr()
	m1.Fid = 9
	m1.RequestMask = proto.AttrMask(0xFFFF)
	putCachedMsg(m1)

	m2 := getCachedTgetattr()
	t.Cleanup(func() { putCachedMsg(m2) })
	if m2.Fid != 0 || m2.RequestMask != 0 {
		t.Errorf("getCachedTgetattr did not zero struct: got %+v, want all zero", *m2)
	}
}

//go:build !nocache

// Package server msgcache_test.go verifies the aliasing invariant for every
// message type cached in msgcache_pools.go (the seven pool.Cache[T]
// instances backed by the generic primitive in internal/pool/cache.go).
//
// Why this test exists (Phase 13, D-06 / D-07):
//
//   - D-06: lock a structural guardrail that every cached struct, once
//     returned to its bounded-chan cache and re-borrowed by a later decode,
//     does not leak prior-decode slice/string data into the new decode's
//     fields.
//   - D-07: the comment in msgcache_pools.go's putCachedMsg (Twalk case)
//     claims "Names is overwritten via make in DecodeFrom so no zeroing
//     needed." This test turns that claim into a CI-enforced contract by
//     asserting, via unsafe.SliceData, that the Twalk.Names backing array
//     is a fresh allocation on every decode. If a future edit replaces
//     `m.Names = make(...)` with `m.Names = append(m.Names[:0], ...)` —
//     which would alias across reuses — the backing-array identity check
//     catches the regression.
//
// unsafe.SliceData is the stdlib replacement for the deprecated
// reflect.SliceHeader (available since Go 1.20; this module requires
// Go 1.26 per go.mod). See pkg.go.dev/unsafe#SliceData.
//
// NOTE: do NOT mark subtests parallel in this file (neither at the outer
// TestCachedMsgReuseDoesNotAliasFields level nor inside the per-type
// helpers). The cache instances in msgcache_pools.go are package-global
// state; running these in parallel would race with each other and with
// any other server test that touches newMessage / {t*}Cache.Get() /
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
// every currently-cached message type (see msgcache_pools.go). Each
// subtest exercises the full decode → putCachedMsg → {t*}Cache.Get()
// → decode cycle and asserts that the second decode's fields reflect
// only the second payload.
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
		{"Tlcreate/fields_zeroed_on_get", testTlcreateAliasing},
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
// whose claim in msgcache_pools.go putCachedMsg (Twalk case) is
// "overwritten via make in DecodeFrom."
// The backing-array pointer check (via unsafe.SliceData) turns that claim
// into a test contract — if DecodeFrom ever regresses to
// append(m.Names[:0], ...) semantics, the pointer would match and this
// subtest would fail.
func testTwalkAliasing(t *testing.T) {
	// Step 1: first decode — 3-element Names slice.
	m1 := twalkCache.Get()
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
	m2 := twalkCache.Get()
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

// testTwriteAliasing verifies the Put-side nil-out in msgcache_pools.go
// putCachedMsg (Twrite case) —
// Twrite.Data aliases pooled bufpool memory on the live request path, so
// leaving it non-nil in the cache would let the next borrower observe a
// recycled bucket buffer on any decode error that aborts before the data
// field is overwritten. No backing-array identity check here: Data is
// intentionally aliased to bufpool memory in production; the test owns
// neither the bufpool lifecycle nor the decode-over-wire buffer, so
// pointer-identity is meaningless for this field.
func testTwriteAliasing(t *testing.T) {
	// Step 1: decode a Twrite with 16 bytes of 0xAA.
	m1 := twriteCache.Get()
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

	// Step 2: Put — must nil Data per msgcache_pools.go putCachedMsg (Twrite case).
	putCachedMsg(m1)
	if m1.Data != nil {
		t.Errorf("putCachedMsg(*Twrite) did not nil Data: len=%d, cap=%d", len(m1.Data), cap(m1.Data))
	}

	// Step 3: re-borrow, decode a shorter Data payload.
	m2 := twriteCache.Get()
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

// testTreadAliasing exercises the generic *m = *new(T) zero-reset in
// internal/pool/cache.go:Cache.Get. Tread has only scalar fields; the
// check is "does Get return a zeroed struct even when a non-zero struct
// was Put."
func testTreadAliasing(t *testing.T) {
	m1 := treadCache.Get()
	m1.Fid, m1.Offset, m1.Count = 10, 100, 1000
	putCachedMsg(m1)

	m2 := treadCache.Get()
	t.Cleanup(func() { putCachedMsg(m2) })
	if m2.Fid != 0 || m2.Offset != 0 || m2.Count != 0 {
		t.Errorf("treadCache.Get() did not zero struct: got %+v, want all zero", *m2)
	}
}

// testTclunkAliasing exercises the generic *m = *new(T) zero-reset in
// internal/pool/cache.go:Cache.Get.
func testTclunkAliasing(t *testing.T) {
	m1 := tclunkCache.Get()
	m1.Fid = 42
	putCachedMsg(m1)

	m2 := tclunkCache.Get()
	t.Cleanup(func() { putCachedMsg(m2) })
	if m2.Fid != 0 {
		t.Errorf("tclunkCache.Get() did not zero struct: got Fid=%d, want 0", m2.Fid)
	}
}

// testTlopenAliasing exercises the generic *m = *new(T) zero-reset in
// internal/pool/cache.go:Cache.Get.
func testTlopenAliasing(t *testing.T) {
	m1 := tlopenCache.Get()
	m1.Fid, m1.Flags = 7, 0xDEADBEEF
	putCachedMsg(m1)

	m2 := tlopenCache.Get()
	t.Cleanup(func() { putCachedMsg(m2) })
	if m2.Fid != 0 || m2.Flags != 0 {
		t.Errorf("tlopenCache.Get() did not zero struct: got %+v, want all zero", *m2)
	}
}

// testTgetattrAliasing exercises the generic *m = *new(T) zero-reset in
// internal/pool/cache.go:Cache.Get.
func testTgetattrAliasing(t *testing.T) {
	m1 := tgetattrCache.Get()
	m1.Fid = 9
	m1.RequestMask = proto.AttrMask(0xFFFF)
	putCachedMsg(m1)

	m2 := tgetattrCache.Get()
	t.Cleanup(func() { putCachedMsg(m2) })
	if m2.Fid != 0 || m2.RequestMask != 0 {
		t.Errorf("tgetattrCache.Get() did not zero struct: got %+v, want all zero", *m2)
	}
}

// testTlcreateAliasing exercises the zero-reset for the Tlcreate cache
// (added in 13-05). Fields: Fid, Name, Flags, Mode, GID. Name is a Go string,
// which is immutable — the shared backing store cannot be mutated through the
// cached struct — so the zero-struct reset in tlcreateCache.Get() is the full
// aliasing defence. The test encodes two distinct Tlcreate frames with
// different Name values and verifies the second decode reflects ONLY the
// second payload's values.
func testTlcreateAliasing(t *testing.T) {
	m1 := tlcreateCache.Get()
	body1 := encodeBody(t, &p9l.Tlcreate{
		Fid:   1,
		Name:  "first-file",
		Flags: 0x0002 | 0x0040, // O_RDWR | O_CREAT
		Mode:  proto.FileMode(0o644),
		GID:   1000,
	})
	decodeBody(t, m1, body1)
	if m1.Name != "first-file" {
		t.Fatalf("first decode: got Name=%q, want first-file", m1.Name)
	}

	putCachedMsg(m1)
	m2 := tlcreateCache.Get()
	t.Cleanup(func() { putCachedMsg(m2) })

	body2 := encodeBody(t, &p9l.Tlcreate{
		Fid:   99,
		Name:  "second",
		Flags: 0,
		Mode:  proto.FileMode(0o600),
		GID:   2000,
	})
	decodeBody(t, m2, body2)

	if m2.Name != "second" {
		t.Errorf("cache reuse leaked Name: got %q, want second", m2.Name)
	}
	if m2.Fid != 99 || m2.Flags != 0 || m2.Mode != proto.FileMode(0o600) || m2.GID != 2000 {
		t.Errorf("cache reuse leaked scalar fields: got %+v, want Fid=99 Flags=0 Mode=0o600 GID=2000", *m2)
	}
}

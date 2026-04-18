package client

import (
	"testing"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// TestRmsgCache_GetFresh verifies that a first Get from an empty cache
// returns a non-nil zero-valued message.
func TestRmsgCache_GetFresh(t *testing.T) {
	t.Parallel()

	m := getCachedRwalk()
	if m == nil {
		t.Fatal("getCachedRwalk returned nil")
	}
	if m.QIDs != nil {
		t.Fatalf("fresh Rwalk.QIDs = %v, want nil", m.QIDs)
	}
}

// TestRmsgCache_RoundTrip verifies that Put followed by Get returns a
// zero-reset pointer (per pool.Cache[T] contract — *m = *new(T) on Get).
func TestRmsgCache_RoundTrip(t *testing.T) {
	t.Parallel()

	m := getCachedRwalk()
	m.QIDs = []proto.QID{{Type: proto.QTDIR, Path: 1}}
	putCachedRMsg(m)

	// After Put, the same pointer should come back out with QIDs zero-reset.
	// Two paths can zero QIDs:
	//   1. putCachedRMsg sets m.QIDs = nil before Put (belt-and-braces, this plan)
	//   2. pool.Cache[T].Get zero-resets via *m = *new(T) (cache contract)
	// Either way, the observable invariant on Get is QIDs == nil.
	m2 := getCachedRwalk()
	if m2.QIDs != nil {
		t.Fatalf("after Put+Get: Rwalk.QIDs = %v, want nil", m2.QIDs)
	}
}

// TestRmsgCache_AliasingRread verifies that Rread.Data is cleared on Put.
// If Data is not cleared, a concurrent peer could observe the stale pointer
// between the Put and the next decode's assignment — the research §6 and
// server/msgcache_pools.go:60 invariant for the T-side.
func TestRmsgCache_AliasingRread(t *testing.T) {
	t.Parallel()

	// Seed the cache with an Rread whose Data points into a test-owned buffer.
	buf := []byte{0xAA, 0xAA, 0xAA, 0xAA}
	m := getCachedRread()
	m.Data = buf
	putCachedRMsg(m)

	// Mutate the test-owned buffer after Put. If putCachedRMsg did not clear
	// Data, a subsequent Get would return an Rread whose Data aliases the
	// now-mutated buffer.
	for i := range buf {
		buf[i] = 0xBB
	}

	m2 := getCachedRread()
	if m2.Data != nil {
		t.Fatalf("after Put+Get: Rread.Data = %v, want nil (Put-side clear required to prevent aliasing)", m2.Data)
	}
}

// TestPutCachedRMsg_UnknownType verifies that putCachedRMsg does not panic
// when given a message type it doesn't cache (Rversion, Rattach, Rflush —
// and T-messages, which are server-side).
func TestPutCachedRMsg_UnknownType(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("putCachedRMsg panicked on unknown type: %v", r)
		}
	}()

	// Tversion is a T-message — client never caches these, should no-op.
	putCachedRMsg(&proto.Tversion{})
	// Rversion is an R-message but is low-volume (once per Conn) — not cached.
	putCachedRMsg(&proto.Rversion{})
	// Rattach is an R-message but low-volume (once per attach) — not cached.
	putCachedRMsg(&proto.Rattach{})
}

// TestPutCachedRMsg_NilArg verifies that putCachedRMsg handles a nil
// argument defensively.
func TestPutCachedRMsg_NilArg(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("putCachedRMsg panicked on nil: %v", r)
		}
	}()

	putCachedRMsg(nil)
}

// TestRmsgCache_AllTypesRoundTrip verifies that every cached type can be
// Put and Get'd without panic. Guards against future additions to the cache
// set that forget a case in the type switch.
func TestRmsgCache_AllTypesRoundTrip(t *testing.T) {
	t.Parallel()

	// Exercise each cache once.
	putCachedRMsg(getCachedRread())
	putCachedRMsg(getCachedRwrite())
	putCachedRMsg(getCachedRwalk())
	putCachedRMsg(getCachedRclunk())
	putCachedRMsg(getCachedRlopen())
	putCachedRMsg(getCachedRlcreate())
	putCachedRMsg(getCachedRlerror())

	// And verify each Get returns non-nil.
	if getCachedRread() == nil {
		t.Error("getCachedRread returned nil")
	}
	if getCachedRwrite() == nil {
		t.Error("getCachedRwrite returned nil")
	}
	if getCachedRwalk() == nil {
		t.Error("getCachedRwalk returned nil")
	}
	if getCachedRclunk() == nil {
		t.Error("getCachedRclunk returned nil")
	}
	if getCachedRlopen() == nil {
		t.Error("getCachedRlopen returned nil")
	}
	if getCachedRlcreate() == nil {
		t.Error("getCachedRlcreate returned nil")
	}
	if getCachedRlerror() == nil {
		t.Error("getCachedRlerror returned nil")
	}

	// Silence p9l import when only type name is needed in godoc.
	_ = (*p9l.Rlerror)(nil)
}

package client

import (
	"sync"

	"github.com/dotwaffle/ninep/proto"
)

// inflightMap tracks per-tag response state for requests awaiting replies.
// Mirrors server/flush.go's inflightMap shape but stores *requestEntry,
// not *requestCtx (server) — the client side has no per-request context
// machinery; the entry just carries a cap-1 response chan and, for the
// 24-03 zero-copy Rread path, an optional caller-supplied dst slice.
//
// Entry-type rationale (24-03 / D-05): pre-24-03 the value type was
// chan proto.Message direct. Replacing it with a *requestEntry adds one
// pointer indirection per lookup but lets the read loop's Rread fast
// path consult entry.dst without a second map probe — and keeps the
// register / deliver / unregister contract byte-identical for non-ZC
// callers (roundTrip, flushAndWait).
//
// Per D-04 (19-CONTEXT): RWMutex-guarded map; read goroutine takes RLock
// for the per-frame lookup; caller goroutines take Lock for register /
// unregister pairs. Per research §4 Pitfall 1, callers MUST register
// BEFORE writing the T-message. Per Pitfall 2, callers MUST unregister
// BEFORE returning the tag to the allocator.
type inflightMap struct {
	mu      sync.RWMutex
	entries map[proto.Tag]*requestEntry
}

// requestEntry is the inflight map's value type. Pre-24-03 the map stored
// `chan proto.Message` directly; the indirection through *requestEntry
// adds the optional dst slice + n out-field for the zero-copy Rread
// path described in 24-RESEARCH.md §Pattern B and 24-CONTEXT.md D-05.
//
// Invariants (all of which existed pre-24-03 for the ch field; dst/n are
// new and additive):
//
//   - ch is cap-1; deliver's send is non-blocking by tag-serialization
//     invariant (one outstanding deliver per tag at a time).
//   - dst == nil on non-ZC requests (registered via register); n is unused.
//   - dst != nil on ZC requests (registered via registerZC); the read loop
//     writes the byte count into entry.n before delivering rreadSentinelOK.
//   - Entries are short-lived: register → deliver → unregister within one
//     roundTrip call. There is no concurrent reader/writer on entry
//     fields outside the read loop's single deliver call.
//
// n is stored inline (not *int) to avoid a per-ReadAt heap allocation —
// readAtZeroCopy holds the *requestEntry by pointer (it's already heap
// because it's stored in the map), and the read loop's write-then-deliver
// happens-before the caller's receive-then-read-n via the cap-1 ch's
// happens-before edge. So the inline field is data-race-free without an
// indirection.
type requestEntry struct {
	ch  chan proto.Message
	dst []byte
	n   int
}

// newInflightMap returns an empty inflightMap.
func newInflightMap() *inflightMap {
	return &inflightMap{entries: make(map[proto.Tag]*requestEntry)}
}

// register allocates a cap-1 response chan and stores a non-ZC entry
// under tag. The returned chan is the caller's receive end; the read
// goroutine delivers into it via deliver. Cap-1 guarantees the read
// goroutine's send in deliver never blocks (research §4 Pitfall 3).
//
// Contract is unchanged from pre-24-03: callers (roundTrip / flushAndWait)
// see no behavior change. The added *requestEntry indirection is internal.
func (im *inflightMap) register(tag proto.Tag) chan proto.Message {
	ch := make(chan proto.Message, 1)
	entry := &requestEntry{ch: ch}
	im.mu.Lock()
	im.entries[tag] = entry
	im.mu.Unlock()
	return ch
}

// registerZC allocates a cap-1 response chan and stores a zero-copy entry
// under tag. The read loop's Rread fast path (read_loop.go) will copy
// the response payload directly into dst[:count] and set entry.n = int(count)
// before posting a sentinel R-message (rreadSentinelOK) on entry.ch.
// This avoids both the proto.Rread.Data alloc inside DecodeFrom AND the
// result-copy in Conn.Read — see 24-03-PLAN §objective.
//
// Returns the entry pointer (so callers can read entry.n after the
// receive on entry.ch unblocks) — entry escapes to the heap once via
// the map insertion below; returning the pointer adds no extra alloc.
//
// dst MUST be non-nil; len(dst) MUST be >= the requested Tread.count.
// If the server returns more than len(dst) bytes (Pitfall 1 in
// 24-RESEARCH.md), the read loop treats it as a protocol error and
// shuts the Conn down rather than silently truncating.
//
// Caller owns dst for the duration of the round trip; do NOT release dst
// back to any pool until ch has delivered (or the Conn has shut down).
func (im *inflightMap) registerZC(tag proto.Tag, dst []byte) *requestEntry {
	entry := &requestEntry{ch: make(chan proto.Message, 1), dst: dst}
	im.mu.Lock()
	im.entries[tag] = entry
	im.mu.Unlock()
	return entry
}

// lookup returns the entry registered for tag, or nil. The read loop
// consults this before allocating an R-message via newRMessage so that
// the zero-copy Rread fast path can be taken when entry.dst != nil.
//
// Safety of using the returned pointer after the RLock is released:
// the register / unregister pair serializes via the cap-1 ch — unregister
// only runs after the caller has received from ch, and the read loop's
// sole deliver call for this tag happens-before the caller's unregister.
// So as long as the read loop is the sole reader of entry.dst / entry.n
// (which it is — only the Rread fast path writes them), no concurrent
// mutation is possible.
func (im *inflightMap) lookup(tag proto.Tag) *requestEntry {
	im.mu.RLock()
	defer im.mu.RUnlock()
	return im.entries[tag]
}

// deliver posts msg to the chan registered under tag. Non-blocking per the
// cap-1 + tag-serialization invariant (research §4 invariant 2). If the tag
// was not registered (Pitfall 10-A: late response after caller ctx cancel
// or shutdown race, or a misbehaving server), msg is returned to its
// R-message cache via putCachedRMsg and then dropped — caller logs
// upstream if desired.
//
// The send happens under RLock to serialize against cancelAll's close(ch)
// call. Without this, the -race detector flags (correctly) that chansend
// and closechan may be concurrent on the same channel when the read loop
// delivers a message while Close is tearing down. The RLock is released
// immediately after the non-blocking select so deliver never parks while
// holding the lock.
//
// Cache reclamation on the drop paths (WR-03): without this, every
// cancellation / shutdown race where the server's reply beat the caller's
// unregister leaked a cached Rread/Rwalk/Rlerror/etc. to GC and slowly
// drained the bounded per-type caches. The cross-package dependency
// inflight → msgcache is accepted here rather than plumbed as a callback,
// because the cache is a package-private implementation detail of the
// same package.
func (im *inflightMap) deliver(tag proto.Tag, msg proto.Message) {
	im.mu.RLock()
	defer im.mu.RUnlock()
	entry := im.entries[tag]
	if entry == nil {
		// Late delivery after unregister (ctx cancel / shutdown race) or
		// an unregistered tag from a misbehaving server. Reclaim the
		// cache slot before dropping.
		putCachedRMsg(msg)
		return
	}
	select {
	case entry.ch <- msg:
	default:
		// Unreachable under correct Phase 19 usage — cap-1 chan +
		// tag-serialization (free-list handoff) means the slot is free.
		// Defense-in-depth for Phase 22 Tflush late-delivery scenarios.
		putCachedRMsg(msg)
	}
}

// unregister removes tag from the map. Called by the caller goroutine
// AFTER receiving from its respCh and BEFORE returning the tag to the
// allocator (Pitfall 2).
func (im *inflightMap) unregister(tag proto.Tag) {
	im.mu.Lock()
	delete(im.entries, tag)
	im.mu.Unlock()
}

// cancelAll closes every registered chan and drops the map to empty. Called
// during Conn shutdown (Plan 19-05 / signalShutdown). After cancelAll, any
// caller blocked on <-respCh unblocks with (zero proto.Message, ok=false);
// callers detect !ok + the Conn's closed state and return ErrClosed.
func (im *inflightMap) cancelAll() {
	im.mu.Lock()
	defer im.mu.Unlock()
	for tag, entry := range im.entries {
		close(entry.ch)
		delete(im.entries, tag)
	}
}

// len returns the current entry count. Used by shutdown-drain logging.
func (im *inflightMap) len() int {
	im.mu.RLock()
	defer im.mu.RUnlock()
	return len(im.entries)
}

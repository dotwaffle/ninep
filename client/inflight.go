package client

import (
	"sync"

	"github.com/dotwaffle/ninep/proto"
)

// inflightMap tracks per-tag response channels for requests awaiting
// replies. Mirrors server/flush.go's inflightMap shape but stores
// chan proto.Message (cap 1) instead of *requestCtx.
//
// Chan-type rationale (plan 19-03 objective): proto.Message is an interface
// holding a pointer-to-concrete-type (e.g. *proto.Rread). A
// `chan proto.Message` already transports the pointer at zero extra
// indirection; `chan *proto.Message` (pointer-to-interface) adds a level
// with no benefit and makes the register→deliver→unregister invariants
// harder to reason about.
//
// Per D-04 (19-CONTEXT): RWMutex-guarded map; read goroutine takes RLock
// for the per-frame lookup; caller goroutines take Lock for register /
// unregister pairs. Per research §4 Pitfall 1, callers MUST register
// BEFORE writing the T-message. Per Pitfall 2, callers MUST unregister
// BEFORE returning the tag to the allocator.
type inflightMap struct {
	mu      sync.RWMutex
	entries map[proto.Tag]chan proto.Message
}

// newInflightMap returns an empty inflightMap.
func newInflightMap() *inflightMap {
	return &inflightMap{entries: make(map[proto.Tag]chan proto.Message)}
}

// register allocates a cap-1 response chan and stores it under tag. The
// returned chan is the caller's receive end; the read goroutine delivers
// into it via deliver. Cap-1 guarantees the read goroutine's send in
// deliver never blocks (research §4 Pitfall 3).
func (im *inflightMap) register(tag proto.Tag) chan proto.Message {
	ch := make(chan proto.Message, 1)
	im.mu.Lock()
	im.entries[tag] = ch
	im.mu.Unlock()
	return ch
}

// deliver posts msg to the chan registered under tag. Non-blocking per the
// cap-1 + tag-serialization invariant (research §4 invariant 2). If the tag
// was not registered (Pitfall 10-A: late response after cancel, or a
// misbehaving server), the msg is silently dropped — caller logs upstream
// if desired.
//
// The send happens under RLock to serialize against cancelAll's close(ch)
// call. Without this, the -race detector flags (correctly) that chansend
// and closechan may be concurrent on the same channel when the read loop
// delivers a message while Close is tearing down. The RLock is released
// immediately after the non-blocking select so deliver never parks while
// holding the lock.
func (im *inflightMap) deliver(tag proto.Tag, msg proto.Message) {
	im.mu.RLock()
	defer im.mu.RUnlock()
	ch := im.entries[tag]
	if ch == nil {
		return
	}
	select {
	case ch <- msg:
	default:
		// Unreachable under correct Phase 19 usage — cap-1 chan +
		// tag-serialization (free-list handoff) means the slot is free.
		// Defense-in-depth for Phase 22 Tflush late-delivery scenarios.
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
	for tag, ch := range im.entries {
		close(ch)
		delete(im.entries, tag)
	}
}

// len returns the current entry count. Used by shutdown-drain logging.
func (im *inflightMap) len() int {
	im.mu.RLock()
	defer im.mu.RUnlock()
	return len(im.entries)
}

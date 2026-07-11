package client

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dotwaffle/ninep/proto"
)

// defaultFlushGrace bounds how long flushAndWait waits for a server to
// acknowledge a Tflush once the caller's context is already cancelled. A
// healthy server answers near-instantly; this only fires for a wedged peer.
const defaultFlushGrace = 30 * time.Second

// flushTagBit marks the reserved upper half of the tag space used for
// Tflush frames. Request tags come from the allocator range
// [1..maxMaxInflight]; each request's Tflush uses the mirror tag
// oldTag|flushTagBit. WithMaxInflight's upper bound of 32766 keeps the mirror
// range below NoTag (compile-time checked in options.go).
const flushTagBit = 0x8000

// flushTagFor returns the reserved Tflush tag for a request tag. Deriving
// the flush tag instead of drawing it from the allocator is what makes
// mass cancellation deadlock-free: when every allocator tag is held by a
// cancelled request, each flushAndWait would otherwise block in acquire
// waiting for a tag that only another blocked flushAndWait could release.
// Each in-flight request sends at most one Tflush, and in-flight request
// tags are unique, so the mirror tag is collision-free by construction.
func flushTagFor(oldTag proto.Tag) proto.Tag {
	return oldTag | flushTagBit
}

// flushAndWait is called from [Conn.roundTrip] when the caller's ctx
// cancels mid-request. It sends Tflush(oldTag) and blocks until the
// FIRST of (original R, Rflush, closeCh) arrives - discarding the
// late-arriving second frame.
//
// Preconditions (enforced by the caller [Conn.roundTrip]):
//
//   - ctx.Err() != nil (caller's ctx is already Done).
//   - oldTag is registered in c.inflight with origCh as its respCh.
//   - Caller has NOT yet unregistered or released oldTag; flushAndWait
//     owns the unregister + release of oldTag on every return path.
//   - c.callerWG.Add(1) is already in effect (roundTrip's defer
//     covers flushAndWait too; no extra bookkeeping needed).
//
// Postconditions (ALL branches, enforced by deferred cleanup):
//
//   - oldTag is unregister()'d BEFORE release()'d.
//
//   - flushTag is unregister()'d (it is a derived mirror tag, never
//     drawn from the allocator, so there is nothing to release).
//
//   - The returned error chain satisfies [errors.Is] for the
//     appropriate sentinels:
//
//     R-first: fmt.Errorf("client: flushed tag %d: %w", oldTag, ctx.Err())
//     Rflush-first: wraps errors.Join(ctx.Err(), ErrFlushed)
//     closeCh: returns ErrClosed unwrapped (losing the ctx cause on
//     the close race is acceptable).
//
// Defer ordering: oldTag's cleanup defer is registered FIRST (outer);
// flushTag's unregister is registered SECOND (inner). Because defers
// run LIFO, flushTag leaves the inflight map before oldTag returns to
// the allocator, so a recycled oldTag can never coexist with its own
// still-registered mirror tag.
//
// Anti-patterns (documented here because they have bitten similar
// helpers elsewhere):
//
//   - Do NOT call [Raw.Tflush] (the public wire-op wrapper). It goes
//     through [Conn.roundTrip], which re-enters the ctx.Done arm on
//     an already-Done ctx and would recurse.
//   - Do NOT draw flushTag from [tagAllocator.acquire]. Under mass
//     cancellation with a saturated allocator, every flushAndWait
//     blocks in acquire waiting for a tag that only another blocked
//     flushAndWait could release: a deadlock held until closeCh. The
//     reserved mirror range (flushTagFor) exists for exactly this.
//   - Do NOT add a raw ctx.Done arm to the inner select: ctx is already
//     Done, so it would fire immediately and release oldTag before the
//     server acknowledges the flush, risking tag-reuse aliasing. The
//     bounded grace timer below is the correct escape for a wedged peer.
//   - The late-arriving second frame is NOT drained by a separate
//     goroutine. [inflightMap.deliver] finds the unregistered tag and
//     drops via [putCachedRMsg] (designed behaviour).
func (c *Conn) flushAndWait(
	ctx context.Context,
	oldTag proto.Tag,
	origCh chan proto.Message,
) (proto.Message, error) {
	// Deferred cleanup for oldTag. Registered FIRST, runs LAST.
	// Unregister BEFORE release.
	defer func() {
		c.inflight.unregister(oldTag)
		c.tags.release(oldTag)
	}()

	// Per the 9P spec, Tflush carries its own tag; the server's Rflush
	// echoes Tflush's tag, NOT oldTag. The tag comes from the reserved
	// mirror range (see flushTagFor), never the allocator: drawing it
	// from the shared free-list deadlocked under mass cancellation, with
	// every saturated caller's flushAndWait blocked in acquire waiting
	// for a tag only another blocked flushAndWait could release.
	flushTag := flushTagFor(oldTag)
	flushCh := c.inflight.register(flushTag)
	// Registered AFTER oldTag defer so it runs FIRST (LIFO).
	defer c.inflight.unregister(flushTag)

	// Send the Tflush frame. writeT handles the isClosed pre-flight
	// + the signalShutdown race at write time. We use writeT directly
	// instead of [Raw.Tflush] to avoid recursing through roundTrip on
	// an already-Done ctx.
	if _, err := c.writeT(flushTag, &proto.Tflush{OldTag: oldTag}); err != nil {
		if c.isClosed() {
			return nil, ErrClosed
		}
		// Transport-level writeT failure on a non-shutting-down Conn:
		// rare (partially-closed socket). Wrap ctx.Err() with the
		// underlying writeT error so errors.Is(err, ctx.Err()) still
		// works.
		return nil, fmt.Errorf(
			"client: flush tag %d: %w (writeT failed: %w)",
			oldTag, ctx.Err(), err,
		)
	}

	// Bound the wait: the caller's ctx is already cancelled, but a
	// wedged-but-TCP-alive peer might never answer the original request OR
	// the Tflush, which would block here until Conn.Close. After a grace
	// period, treat the peer as dead and tear the connection down so the tag
	// is reclaimed via shutdown rather than aliased onto a reused tag.
	grace := time.NewTimer(c.flushGrace)
	defer grace.Stop()

	// Wait for the first frame. The late-arriving second frame lands
	// in [inflightMap.deliver]; because our defers have already run
	// (on the return path below), the second frame's tag is
	// unregistered and deliver drops it via putCachedRMsg.
	//
	// Buffered-race note: origCh and flushCh are both cap-1 buffered.
	// If the read loop delivers BOTH frames before this
	// select fires, Go picks the winning arm uniformly at random; the
	// OTHER frame is already buffered and cannot be salvaged by
	// deliver's unregistered-tag drop path (that path only catches
	// frames that arrive AFTER the defer unregister). Each arm below
	// therefore performs a non-blocking drain of its peer channel and
	// reclaims the frame via putCachedRMsg. Without this, under
	// sustained cancellation against an RflushSendImmediately server
	// the bounded Rread/Rwalk/Rlerror caches slowly drain to GC.
	select {
	case r, ok := <-origCh:
		if !ok {
			// cancelAll closed origCh during shutdown race.
			return nil, ErrClosed
		}
		// Original R arrived first (R-first path). Caller wanted out -
		// discard the data but reclaim the pooled R-message slot.
		// putCachedRMsg is a no-op for types not in the cache set.
		putCachedRMsg(r)
		// Non-blocking drain of flushCh: Rflush is uncached today
		// (falls to the default arm of putCachedRMsg) so this is a
		// no-op for correctness, but keeps the two arms symmetric if
		// Rflush ever joins the cache set.
		select {
		case rf, ok := <-flushCh:
			if ok {
				putCachedRMsg(rf)
			}
		default:
		}
		return nil, fmt.Errorf(
			"client: flushed tag %d: %w", oldTag, ctx.Err(),
		)

	case r, ok := <-flushCh:
		if !ok {
			return nil, ErrClosed
		}
		// Rflush arrived first (Rflush-first path). Reclaim the
		// Rflush struct (putCachedRMsg is a no-op for Rflush per
		// msgcache.go but the call keeps the return path uniform with
		// the origCh arm).
		putCachedRMsg(r)
		// Non-blocking drain of origCh: if the original R raced into
		// origCh before the select fired, it is invisible to
		// deliver's unregistered-tag drop path (the defer hasn't run
		// yet) and would leak to GC, slowly draining the bounded
		// Rread/Rwalk/Rlerror caches. Reclaim it here.
		select {
		case orig, ok := <-origCh:
			if ok {
				putCachedRMsg(orig)
			}
		default:
		}
		return nil, fmt.Errorf(
			"client: flushed tag %d: %w", oldTag,
			errors.Join(ctx.Err(), ErrFlushed),
		)

	case <-c.closeCh:
		// closeCh wins the race. Returning ErrClosed unwrapped is
		// acceptable - callers MUST match on ErrClosed when they care
		// about conn-level shutdown.
		return nil, ErrClosed

	case <-grace.C:
		// The peer answered neither the original request nor the Tflush
		// within the grace period: it is wedged. Tear the connection down so
		// the caller is released and the tag is reclaimed via cancelAll.
		c.signalShutdown()
		return nil, fmt.Errorf(
			"client: flush tag %d: %w (no server response within grace)", oldTag, ctx.Err(),
		)
	}
}

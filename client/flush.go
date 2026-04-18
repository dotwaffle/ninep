package client

import (
	"context"
	"errors"
	"fmt"

	"github.com/dotwaffle/ninep/proto"
)

// flushAndWait is called from [Conn.roundTrip] when the caller's ctx
// cancels mid-request. It sends Tflush(oldTag) and blocks until the
// FIRST of (original R, Rflush, closeCh) arrives — discarding the
// late-arriving second frame per D-06.
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
//   - oldTag is unregister()'d BEFORE release()'d (Pitfall 2).
//
//   - flushTag (if acquired) is unregister()'d BEFORE release()'d
//     (Pitfall 2).
//
//   - The returned error chain satisfies [errors.Is] for the
//     appropriate sentinels per D-05:
//
//     R-first: fmt.Errorf("9p: flushed tag %d: %w", oldTag, ctx.Err())
//     Rflush-first: wraps errors.Join(ctx.Err(), ErrFlushed)
//     closeCh: returns ErrClosed unwrapped (Pitfall 5 says losing the
//     ctx cause on the close race is acceptable).
//
// Defer ordering: oldTag's cleanup defer is registered FIRST (outer);
// flushTag's cleanup defer is registered SECOND (inner). Because
// defers run LIFO, flushTag is released before oldTag. This is
// intentional — the newer tag (flushTag) returns to the allocator
// first so there's no window where a recycled oldTag could collide
// with a still-registered flushTag.
//
// Anti-patterns (documented here because they have bitten similar
// helpers elsewhere; see .planning/phases/22/22-RESEARCH.md §Pitfalls):
//
//   - Do NOT call [Conn.Flush] (the public wire-op wrapper). It goes
//     through [Conn.roundTrip], which re-enters the ctx.Done arm on
//     an already-Done ctx and would recurse (Pitfall 1).
//   - Do NOT pass ctx to [tagAllocator.acquire]. ctx is already Done
//     here — acquire would return ctx.Err immediately without ever
//     handing out a tag (Pitfall 3b). Use [context.Background] with
//     c.closeCh as the abort channel.
//   - Do NOT add a ctx.Done arm to the inner select. ctx is already
//     Done; re-selecting on it is a dead branch (Pitfall 6).
//   - The late-arriving second frame is NOT drained by a separate
//     goroutine. [inflightMap.deliver] finds the unregistered tag and
//     drops via [putCachedRMsg] (Pitfall 7 — designed behaviour).
func (c *Conn) flushAndWait(
	ctx context.Context,
	oldTag proto.Tag,
	origCh chan proto.Message,
) (proto.Message, error) {
	// Deferred cleanup for oldTag. Registered FIRST, runs LAST.
	// Unregister BEFORE release — Pitfall 2.
	defer func() {
		c.inflight.unregister(oldTag)
		c.tags.release(oldTag)
	}()

	// Acquire a tag for the Tflush frame itself. Per the 9P spec,
	// Tflush carries its own tag; the server's Rflush echoes Tflush's
	// tag, NOT oldTag. We cannot reuse oldTag here.
	//
	// ctx parent is context.Background, NOT the caller's ctx: the
	// caller's ctx is ALREADY Done and acquire's select would return
	// ctx.Err immediately before handing out a tag (Pitfall 3b).
	// closeCh remains the only abort path — if the Conn is shutting
	// down during flush setup, acquire returns ErrClosed.
	flushTag, err := c.tags.acquire(context.Background(), c.closeCh)
	if err != nil {
		// Only failure mode: closeCh fired during acquire. Return the
		// allocator's error (ErrClosed) unwrapped; deferred oldTag
		// cleanup still runs.
		return nil, err
	}
	flushCh := c.inflight.register(flushTag)
	// Registered AFTER oldTag defer so it runs FIRST (LIFO).
	defer func() {
		c.inflight.unregister(flushTag)
		c.tags.release(flushTag)
	}()

	// Send the Tflush frame. writeT handles the isClosed pre-flight
	// + the signalShutdown race at write time. We use writeT directly
	// instead of [Conn.Flush] to avoid recursing through roundTrip on
	// an already-Done ctx.
	if err := c.writeT(flushTag, &proto.Tflush{OldTag: oldTag}); err != nil {
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

	// Wait for the first frame. The late-arriving second frame lands
	// in [inflightMap.deliver]; because our defers have already run
	// (on the return path below), the second frame's tag is
	// unregistered and deliver drops it via putCachedRMsg (Pitfall 7).
	select {
	case r, ok := <-origCh:
		if !ok {
			// cancelAll closed origCh during shutdown race.
			return nil, ErrClosed
		}
		// Original R arrived first (D-05 R-first path). Caller wanted
		// out — discard the data (D-07) but reclaim the pooled
		// R-message slot (Q1 resolution, Phase 19 WR-03 pattern).
		// putCachedRMsg is a no-op for types not in the cache set.
		putCachedRMsg(r)
		return nil, fmt.Errorf(
			"9p: flushed tag %d: %w", oldTag, ctx.Err(),
		)

	case r, ok := <-flushCh:
		if !ok {
			return nil, ErrClosed
		}
		// Rflush arrived first (D-05 Rflush-first path). Reclaim the
		// Rflush struct (putCachedRMsg is a no-op for Rflush per
		// msgcache.go but the call keeps the return path uniform with
		// the origCh arm).
		putCachedRMsg(r)
		return nil, fmt.Errorf(
			"9p: flushed tag %d: %w", oldTag,
			errors.Join(ctx.Err(), ErrFlushed),
		)

	case <-c.closeCh:
		// Pitfall 5: closeCh wins the race. Returning ErrClosed
		// unwrapped is acceptable — callers MUST match on ErrClosed
		// when they care about conn-level shutdown.
		return nil, ErrClosed
	}
}

package client

import (
	"context"
	"errors"
	"fmt"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
	"github.com/dotwaffle/ninep/proto/p9u"
)

// roundTrip is the shared dispatch helper used by every op method on *Conn.
// It enforces the ordering invariants from research §4:
//
//  1. Pre-flight isClosed check — short-circuit to ErrClosed before paying
//     the tagAllocator round trip (pitfall 10-C).
//  2. callerWG.Add(1) / defer Done — Close() waits for callers to drain
//     before shutting the read goroutine (Plan 19-05 contract).
//  3. tagAllocator.acquire — blocks on ctx, closeCh, or free-list slot.
//  4. inflight.register BEFORE writeT — pitfall 1 (register-before-send).
//  5. writeT — encode + writev under writeMu.
//  6. Wait on respCh / ctx.Done / closeCh.
//  7. unregister(tag) BEFORE release(tag) — pitfall 2 (tag-reuse race).
//  8. release(tag) — return to free-list.
//
// Error paths preserve the unregister-before-release ordering. On writeT
// failure, the tag is released after unregistering so the caller observes
// the real write error (not a tag-leak consequence).
//
// Phase 19 does not send Tflush on ctx cancellation. Phase 22 (CLIENT-04)
// wires ctx→Tflush. For now, ctx cancel returns ctx.Err() immediately; a
// subsequent server response with the cancelled tag is silently dropped by
// inflight.deliver (pitfall 10-A).
//
// Returns the decoded R-message as a proto.Message value. The caller is
// responsible for calling toError first (to translate Rlerror/Rerror) and
// then type-asserting to the expected concrete type.
func (c *Conn) roundTrip(ctx context.Context, msg proto.Message) (proto.Message, error) {
	if c.isClosed() {
		return nil, ErrClosed
	}
	c.callerWG.Add(1)
	defer c.callerWG.Done()

	tag, err := c.tags.acquire(ctx, c.closeCh)
	if err != nil {
		return nil, err
	}

	// Register BEFORE writeT — pitfall 1.
	respCh := c.inflight.register(tag)

	if err := c.writeT(tag, msg); err != nil {
		// Pitfall 2 ordering preserved on error paths: unregister, then
		// release.
		c.inflight.unregister(tag)
		c.tags.release(tag)
		return nil, err
	}

	// Wait for response.
	select {
	case r, ok := <-respCh:
		if !ok {
			// Channel closed by inflight.cancelAll — Conn is shutting down.
			// The read goroutine has signalled shutdown; our caller observes
			// ErrClosed. The tag is released so no leak.
			c.tags.release(tag)
			return nil, ErrClosed
		}
		// Unregister BEFORE release — pitfall 2.
		c.inflight.unregister(tag)
		c.tags.release(tag)
		return r, nil
	case <-ctx.Done():
		c.inflight.unregister(tag)
		c.tags.release(tag)
		return nil, ctx.Err()
	case <-c.closeCh:
		c.inflight.unregister(tag)
		c.tags.release(tag)
		return nil, ErrClosed
	}
}

// toError translates an R-message into a *Error if it represents a
// server-reported failure. Rlerror (.L) populates only Errno; Rerror (.u)
// populates both Errno and Msg. Returns nil for any other message type —
// the caller treats that as a normal response and type-asserts.
//
// Per D-13 (.planning/phases/19/19-CONTEXT.md) callers always route through
// toError before type-asserting, so the two dialects' error shapes are
// unified at the ops boundary and user code uses a single errors.Is pattern
// against proto.Errno constants regardless of negotiated dialect.
func toError(msg proto.Message) error {
	switch r := msg.(type) {
	case *p9l.Rlerror:
		return &Error{Errno: r.Ecode}
	case *p9u.Rerror:
		return &Error{Errno: r.Errno, Msg: r.Ename}
	}
	return nil
}

// expectRType returns an error if msg's concrete MessageType is not one of
// wantTypes. Used as a belt-and-braces guard by op methods after toError,
// to surface server-side dialect or wire bugs as a descriptive error rather
// than a silent type-assertion panic or nil return.
//
// Nil msg (should never happen after a successful roundTrip) returns a
// distinct error so the caller can log-diagnose.
func expectRType(msg proto.Message, wantTypes ...proto.MessageType) error {
	if msg == nil {
		return errors.New("client: nil response")
	}
	got := msg.Type()
	for _, w := range wantTypes {
		if got == w {
			return nil
		}
	}
	return fmt.Errorf("client: unexpected response type %v", got)
}

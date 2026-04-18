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

// Attach associates fid with the root of the file tree named by aname and
// establishes the session for user uname. Per D-17/D-18 Phase 19 supports
// only afid=NoFid (no authentication); Tauth is not implemented. aname
// selects the mount point, server-defined; the empty string is the
// conventional "default" root.
//
// Returns the root QID on success, or a *Error translated from Rlerror/Rerror
// on server-side failure.
func (c *Conn) Attach(ctx context.Context, fid proto.Fid, uname, aname string) (proto.QID, error) {
	req := &proto.Tattach{
		Fid:   fid,
		Afid:  proto.NoFid,
		Uname: uname,
		Aname: aname,
	}
	resp, err := c.roundTrip(ctx, req)
	if err != nil {
		return proto.QID{}, err
	}
	if err := toError(resp); err != nil {
		return proto.QID{}, err
	}
	r, ok := resp.(*proto.Rattach)
	if !ok {
		return proto.QID{}, fmt.Errorf("client: expected Rattach, got %v", resp.Type())
	}
	// Rattach is not cached (cold path; once per Attach) but go through
	// putCachedRMsg anyway so future cache-additions do not silently miss
	// this return path.
	qid := r.QID
	putCachedRMsg(resp)
	return qid, nil
}

// Walk descends from fid along names, creating newFid at the final element.
// An empty names slice clones fid into newFid without navigating. Returns
// one QID per successfully walked element.
//
// The returned []proto.QID is caller-owned — it is copied out of the pooled
// Rwalk struct before the struct is returned to the cache, so callers may
// retain the slice indefinitely.
func (c *Conn) Walk(ctx context.Context, fid, newFid proto.Fid, names []string) ([]proto.QID, error) {
	req := &proto.Twalk{Fid: fid, NewFid: newFid, Names: names}
	resp, err := c.roundTrip(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := toError(resp); err != nil {
		return nil, err
	}
	r, ok := resp.(*proto.Rwalk)
	if !ok {
		return nil, fmt.Errorf("client: expected Rwalk, got %v", resp.Type())
	}
	// Copy out before cache return — Rwalk.QIDs aliases a decoder-allocated
	// slice that the cache returns to a zero-reset state on next Get.
	qids := make([]proto.QID, len(r.QIDs))
	copy(qids, r.QIDs)
	putCachedRMsg(resp)
	return qids, nil
}

// Clunk releases fid. After a successful clunk, fid is no longer valid;
// the server deallocates any associated state. Errors from Rlerror/Rerror
// surface as *Error; type-mismatch as a descriptive error.
func (c *Conn) Clunk(ctx context.Context, fid proto.Fid) error {
	req := &proto.Tclunk{Fid: fid}
	resp, err := c.roundTrip(ctx, req)
	if err != nil {
		return err
	}
	if err := toError(resp); err != nil {
		return err
	}
	if _, ok := resp.(*proto.Rclunk); !ok {
		return fmt.Errorf("client: expected Rclunk, got %v", resp.Type())
	}
	putCachedRMsg(resp)
	return nil
}

// Flush asks the server to abort the request identified by oldTag. Per the
// 9P spec the server responds with Rflush regardless of whether oldTag
// matches an outstanding request. As such, a nil return does NOT confirm
// the original request was cancelled — the request may have completed
// before Flush was received.
//
// Phase 19 does not auto-invoke Flush on ctx cancellation; that wiring
// lives in Phase 22 (CLIENT-04). This method is the raw wire-level
// primitive for callers that need it directly.
func (c *Conn) Flush(ctx context.Context, oldTag proto.Tag) error {
	req := &proto.Tflush{OldTag: oldTag}
	resp, err := c.roundTrip(ctx, req)
	if err != nil {
		return err
	}
	if err := toError(resp); err != nil {
		return err
	}
	if _, ok := resp.(*proto.Rflush); !ok {
		return fmt.Errorf("client: expected Rflush, got %v", resp.Type())
	}
	return nil
}

// Read reads up to count bytes from fid starting at offset. Returns the
// bytes actually read, which may be fewer than count (EOF or short read).
//
// The returned slice is caller-owned — it is copied out of the pooled
// Rread struct (whose Data field aliases a bucket buffer from bufpool)
// before the struct is returned to the cache. Callers may retain the
// slice indefinitely.
//
// Read does NOT clamp count to the negotiated msize or the file's iounit.
// Callers that need throughput-optimal chunking should consult the iounit
// returned by Lopen/Open and size their reads accordingly; passing an
// over-large count results in whatever the server chooses to return (many
// servers clamp silently).
func (c *Conn) Read(ctx context.Context, fid proto.Fid, offset uint64, count uint32) ([]byte, error) {
	req := &proto.Tread{Fid: fid, Offset: offset, Count: count}
	resp, err := c.roundTrip(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := toError(resp); err != nil {
		return nil, err
	}
	r, ok := resp.(*proto.Rread)
	if !ok {
		return nil, fmt.Errorf("client: expected Rread, got %v", resp.Type())
	}
	// Copy Data out of the pooled Rread. putCachedRMsg nil's Data before
	// returning to the cache (aliasing invariant), so the backing buffer is
	// reusable by the next Rread borrower immediately.
	data := make([]byte, len(r.Data))
	copy(data, r.Data)
	putCachedRMsg(resp)
	return data, nil
}

// Write writes data to fid starting at offset. Returns the number of bytes
// the server reports as written (may be fewer than len(data)).
func (c *Conn) Write(ctx context.Context, fid proto.Fid, offset uint64, data []byte) (uint32, error) {
	req := &proto.Twrite{Fid: fid, Offset: offset, Data: data}
	resp, err := c.roundTrip(ctx, req)
	if err != nil {
		return 0, err
	}
	if err := toError(resp); err != nil {
		return 0, err
	}
	r, ok := resp.(*proto.Rwrite)
	if !ok {
		return 0, fmt.Errorf("client: expected Rwrite, got %v", resp.Type())
	}
	count := r.Count
	putCachedRMsg(resp)
	return count, nil
}

package client

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
	"github.com/dotwaffle/ninep/proto/p9u"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// exchange is the dispatch skeleton shared by [Conn.roundTrip] and
// [Conn.readAtZeroCopy]. It enforces the following ordering invariants:
//
//  1. callerWG.Add(1) / defer Done - Close() waits for callers to drain
//     before shutting the read goroutine.
//  2. tagAllocator.acquire - blocks on ctx, closeCh, or free-list slot.
//  3. Inflight registration BEFORE writeT (register-before-send). A nil
//     dst registers a plain entry; a non-nil dst registers the zero-copy
//     Rread entry, whose payload the read loop copies straight into dst.
//  4. writeT - encode + writev under writeMu.
//  5. Wait on the response chan / ctx.Done / closeCh.
//  6. unregister(tag) BEFORE release(tag) (tag-reuse race avoidance).
//  7. release(tag) - return to free-list.
//
// Error paths preserve the unregister-before-release ordering. On writeT
// failure, the tag is released after unregistering so the caller observes
// the real write error (not a tag-leak consequence).
//
// Ctx cancellation enters [Conn.flushAndWait], which sends Tflush(tag)
// and blocks for the first frame among (original R, Rflush, closeCh).
// flushAndWait owns the cleanup of both the original tag AND the
// flushTag it derives. The returned error wraps ctx.Err() via %w so
// errors.Is chains work; on the Rflush-first path, [ErrFlushed] is
// also in the chain. Late-arriving second frames are dropped by
// inflight.deliver's unregistered-tag path.
//
// n mirrors the ZC entry's byte count: it is meaningful only when dst
// was non-nil and the returned message is rreadSentinelOK (the read
// loop writes entry.n before delivering; the cap-1 chan send/receive
// edge makes the read here race-free). Callers own the returned
// message's interpretation: toError translation, type assertion, and
// the cache return.
func (c *Conn) exchange(ctx context.Context, span trace.Span, msg proto.Message, dst []byte) (resp proto.Message, n int, err error) {
	c.callerWG.Add(1)
	defer c.callerWG.Done()

	tag, err := c.tags.acquire(ctx, c.closeCh)
	if err != nil {
		c.recordError(span, err)
		return nil, 0, err
	}

	// Register BEFORE writeT.
	var respCh chan proto.Message
	var entry *requestEntry
	if dst == nil {
		respCh = c.inflight.register(tag)
	} else {
		entry = c.inflight.registerZC(tag, dst)
		respCh = entry.ch
	}

	frameSize, err := c.writeT(tag, msg)
	if err != nil {
		// Unregister-before-release ordering preserved on error paths.
		c.inflight.unregister(tag)
		c.tags.release(tag)
		// If the Conn is shutting down (signalShutdown has fired), a
		// writeT failure is almost certainly the result of the shutdown
		// racing the write - surface as ErrClosed so callers see a
		// consistent shutdown signal rather than the transport-specific
		// io.ErrClosedPipe / net.ErrClosed wrapper.
		if c.isClosed() {
			c.recordError(span, ErrClosed)
			return nil, 0, ErrClosed
		}
		c.recordError(span, err)
		return nil, 0, err
	}
	c.recordRequestSize(ctx, frameSize)

	// Wait for response.
	select {
	case r, ok := <-respCh:
		if !ok {
			// Channel closed by inflight.cancelAll - Conn is shutting down.
			// The read goroutine has signalled shutdown; our caller observes
			// ErrClosed. The tag is released so no leak.
			//
			// unregister is called BEFORE release to keep the ordering
			// uniform with the peer branches. cancelAll already deleted
			// the entry today, so this is a no-op map lookup under Lock -
			// but relying on that coupling would silently leak the entry
			// if cancelAll is ever refactored to delay the delete (e.g.
			// for a graceful-drain diagnostic pass). unregister is
			// idempotent (map delete of a missing key).
			c.inflight.unregister(tag)
			c.tags.release(tag)
			c.recordError(span, ErrClosed)
			return nil, 0, ErrClosed
		}
		// Unregister BEFORE release.
		c.inflight.unregister(tag)
		c.tags.release(tag)
		if entry != nil {
			n = entry.n
		}
		return r, n, nil
	case <-ctx.Done():
		// Delegate to flushAndWait, which sends Tflush(tag) and owns
		// the unregister + release of tag (and the derived flushTag).
		// The returned error wraps ctx.Err() so
		// errors.Is(err, context.Canceled) / context.DeadlineExceeded
		// work; on the Rflush-first path, ErrFlushed is also in the
		// chain. On the ZC path any original R that raced in is
		// reclaimed by flushAndWait's drain arms (rreadSentinelOK is a
		// no-op there per the sentinel guard in putCachedRMsg).
		r, ferr := c.flushAndWait(ctx, tag, respCh)
		if ferr != nil {
			c.recordError(span, ferr)
		}
		return r, 0, ferr
	case <-c.closeCh:
		c.inflight.unregister(tag)
		c.tags.release(tag)
		c.recordError(span, ErrClosed)
		return nil, 0, ErrClosed
	}
}

// roundTrip is the shared dispatch helper used by every op method on
// *Conn: a pre-flight isClosed check, span + metric setup, then the
// [Conn.exchange] skeleton with a plain (non-zero-copy) registration.
//
// Returns the decoded R-message as a proto.Message value. The caller is
// responsible for calling toError first (to translate Rlerror/Rerror) and
// then type-asserting to the expected concrete type; [rtrip] bundles
// those steps.
func (c *Conn) roundTrip(ctx context.Context, msg proto.Message) (proto.Message, error) {
	if c.isClosed() {
		return nil, ErrClosed
	}

	opName := msg.Type().String()
	ctx, span := c.startSpan(ctx, opName, msg)
	defer span.End()

	if c.meterEnabled {
		c.inst.activeReqs.Add(ctx, 1)
		defer c.inst.activeReqs.Add(ctx, -1)
	}

	start := time.Now()
	resp, _, err := c.exchange(ctx, span, msg, nil)
	if err != nil {
		return nil, err
	}

	elapsed := time.Since(start).Seconds()
	c.recordResponse(ctx, msg.Type(), elapsed, resp)
	if isErrorResponse(resp) {
		span.SetStatus(codes.Error, opName)
	}
	return resp, nil
}

// toError translates an R-message into a *Error if it represents a
// server-reported failure. Rlerror (.L) populates only Errno; Rerror (.u)
// populates both Errno and Msg. Returns nil for any other message type -
// the caller treats that as a normal response and type-asserts.
//
// Callers always route through toError before type-asserting, so the
// two dialects' error shapes are unified at the ops boundary and user
// code uses a single errors.Is pattern against proto.Errno constants
// regardless of negotiated dialect.
//
// Ownership: when toError returns a non-nil error, it has already
// returned msg to its R-message cache via putCachedRMsg. The caller
// MUST NOT touch msg after observing a non-nil return - the fields
// have been reset and the pointer may already have been handed to
// another borrower. When toError returns nil, msg is left intact for
// the caller to type-assert and later return to the cache.
func toError(msg proto.Message) error {
	switch r := msg.(type) {
	case *p9l.Rlerror:
		e := &Error{Errno: r.Ecode}
		putCachedRMsg(msg)
		return e
	case *p9u.Rerror:
		e := &Error{Errno: r.Errno, Msg: r.Ename}
		putCachedRMsg(msg)
		return e
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
	if slices.Contains(wantTypes, got) {
		return nil
	}
	return fmt.Errorf("client: unexpected response type %v", got)
}

// tattach implements [Raw.Tattach]; [Conn.Attach] wraps it to return a
// *File with an allocator-owned fid.
func (c *Conn) tattach(ctx context.Context, fid proto.Fid, uname, aname string) (proto.QID, error) {
	req := &proto.Tattach{
		Fid:   fid,
		Afid:  proto.NoFid,
		Uname: uname,
		Aname: aname,
	}
	r, err := rtrip[*proto.Rattach](ctx, c, req)
	if err != nil {
		return proto.QID{}, err
	}
	// Rattach is not cached (cold path; once per Attach) but go through
	// putCachedRMsg anyway so future cache-additions do not silently miss
	// this return path.
	qid := r.QID
	putCachedRMsg(r)
	return qid, nil
}

// twalk implements [Raw.Twalk].
func (c *Conn) twalk(ctx context.Context, fid, newFid proto.Fid, names []string) ([]proto.QID, error) {
	req := &proto.Twalk{Fid: fid, NewFid: newFid, Names: names}
	r, err := rtrip[*proto.Rwalk](ctx, c, req)
	if err != nil {
		return nil, err
	}
	// Copy out before cache return -- Rwalk.QIDs aliases a decoder-allocated
	// slice that the cache returns to a zero-reset state on next Get.
	qids := make([]proto.QID, len(r.QIDs))
	copy(qids, r.QIDs)
	putCachedRMsg(r)
	return qids, nil
}

// tclunk implements [Raw.Tclunk].
func (c *Conn) tclunk(ctx context.Context, fid proto.Fid) error {
	r, err := rtrip[*proto.Rclunk](ctx, c, &proto.Tclunk{Fid: fid})
	if err != nil {
		return err
	}
	putCachedRMsg(r)
	return nil
}

// tflush implements [Raw.Tflush].
func (c *Conn) tflush(ctx context.Context, oldTag proto.Tag) error {
	r, err := rtrip[*proto.Rflush](ctx, c, &proto.Tflush{OldTag: oldTag})
	if err != nil {
		return err
	}
	// Rflush is not cached today (it falls to the default arm of
	// putCachedRMsg), but route it through anyway so a future
	// cache addition does not silently miss this success path.
	putCachedRMsg(r)
	return nil
}

// tread implements [Raw.Tread].
func (c *Conn) tread(ctx context.Context, fid proto.Fid, offset uint64, count uint32) ([]byte, error) {
	req := &proto.Tread{Fid: fid, Offset: offset, Count: count}
	r, err := rtrip[*proto.Rread](ctx, c, req)
	if err != nil {
		return nil, err
	}
	if uint64(len(r.Data)) > uint64(count) {
		err := fmt.Errorf("client: Rread count %d exceeds requested count %d", len(r.Data), count)
		putCachedRMsg(r)
		c.signalShutdown()
		return nil, err
	}
	// Copy Data out of the pooled Rread. putCachedRMsg nil's Data before
	// returning to the cache (aliasing invariant), so the backing buffer is
	// reusable by the next Rread borrower immediately.
	data := make([]byte, len(r.Data))
	copy(data, r.Data)
	putCachedRMsg(r)
	return data, nil
}

// readAtZeroCopy issues a Tread whose Rread response is decoded directly
// into dst[:count] by the read loop's zero-copy fast path. Returns the
// number of bytes written into dst.
//
// This is the Payloader-symmetric peer of [Raw.Tread]. Where
// tread pays two allocs per round trip - Rread.Data inside
// proto.Rread.DecodeFrom plus a result-copy in tread itself - this
// helper pays neither: the read loop copies the response payload
// directly from its pooled body buffer into the caller's dst, and
// signals success via the rreadSentinelOK singleton (no Rread cache
// slot consumed).
//
// tread is intentionally NOT removed: Raw.Tread consumers and any
// caller without a pre-allocated destination still use it. File.ReadAt
// routes through readAtZeroCopy because the caller's dst is always
// available there.
//
// dst MUST have len(dst) >= count. The read loop enforces this with a
// protocol-error path (signalShutdown) if the server returns more than
// len(dst) bytes - so the caller's guarantee prevents accidental
// truncation-without-error.
//
// Returns (0, err) on tag-acquisition, write, or context errors - dst
// is untouched in those paths. Returns (n, nil) on success where n is
// the bytes actually read (<= count; may be 0 at EOF).
//
// Ctx-cancel semantics preserved: ctx.Done -> flushAndWait(ctx, tag,
// respCh). The entire response body is received before the caller's
// select runs, so dst is either fully written or completely untouched
// - there is no mid-write cancel window to corrupt the caller's
// buffer.
func (c *Conn) readAtZeroCopy(ctx context.Context, fid proto.Fid, offset uint64, count uint32, dst []byte) (int, error) {
	if c.isClosed() {
		return 0, ErrClosed
	}

	opName := "Tread"
	req := &proto.Tread{Fid: fid, Offset: offset, Count: count}
	ctx, span := c.startSpan(ctx, opName, req)
	defer span.End()

	if c.meterEnabled {
		c.inst.activeReqs.Add(ctx, 1)
		defer c.inst.activeReqs.Add(ctx, -1)
	}

	start := time.Now()
	resp, n, err := c.exchange(ctx, span, req, dst)
	if err != nil {
		return 0, err
	}

	elapsed := time.Since(start).Seconds()
	// resp is one of: rreadSentinelOK (zero-copy fast path success) or
	// an error R-message (Rlerror/Rerror). Defensive type-check at the
	// end catches a misbehaving server returning Rread (cached path)
	// or some other unexpected R-type -- never panic, always return err.
	if resp == rreadSentinelOK {
		c.recordZCResponse(ctx, proto.TypeTread, elapsed, n)
		return n, nil
	}
	if err := toError(resp); err != nil {
		c.recordError(span, err)
		return 0, err
	}

	// Defensive: server (or test mock) sent a non-sentinel Rread or
	// some other unexpected R-type on this tag. Recycle the message
	// and surface a descriptive error.
	err = fmt.Errorf("client: readAtZeroCopy: expected Rread sentinel or Rlerror/Rerror, got %v", resp.Type())
	c.recordError(span, err)
	putCachedRMsg(resp)
	return 0, err
}

// twrite implements [Raw.Twrite].
func (c *Conn) twrite(ctx context.Context, fid proto.Fid, offset uint64, data []byte) (uint32, error) {
	req := &proto.Twrite{Fid: fid, Offset: offset, Data: data}
	r, err := rtrip[*proto.Rwrite](ctx, c, req)
	if err != nil {
		return 0, err
	}
	count := r.Count
	if uint64(count) > uint64(len(data)) {
		err := fmt.Errorf("client: Rwrite count %d exceeds write size %d", count, len(data))
		putCachedRMsg(r)
		c.signalShutdown()
		return 0, err
	}
	putCachedRMsg(r)
	return count, nil
}

// tlopen implements [Raw.Tlopen].
func (c *Conn) tlopen(ctx context.Context, fid proto.Fid, flags uint32) (proto.QID, uint32, error) {
	if err := c.requireDialect(protocolL, "Lopen"); err != nil {
		return proto.QID{}, 0, err
	}
	r, err := rtrip[*p9l.Rlopen](ctx, c, &p9l.Tlopen{Fid: fid, Flags: flags})
	if err != nil {
		return proto.QID{}, 0, err
	}
	qid, iou := r.QID, r.IOUnit
	putCachedRMsg(r)
	return qid, iou, nil
}

// tlcreate implements [Raw.Tlcreate].
func (c *Conn) tlcreate(ctx context.Context, fid proto.Fid, name string, flags uint32, mode proto.FileMode, gid uint32) (proto.QID, uint32, error) {
	if err := c.requireDialect(protocolL, "Lcreate"); err != nil {
		return proto.QID{}, 0, err
	}
	req := &p9l.Tlcreate{
		Fid:   fid,
		Name:  name,
		Flags: flags,
		Mode:  mode,
		GID:   gid,
	}
	r, err := rtrip[*p9l.Rlcreate](ctx, c, req)
	if err != nil {
		return proto.QID{}, 0, err
	}
	qid, iou := r.QID, r.IOUnit
	putCachedRMsg(r)
	return qid, iou, nil
}

// topen implements [Raw.Topen].
func (c *Conn) topen(ctx context.Context, fid proto.Fid, mode uint8) (proto.QID, uint32, error) {
	if err := c.requireDialect(protocolU, "Open"); err != nil {
		return proto.QID{}, 0, err
	}
	r, err := rtrip[*p9u.Ropen](ctx, c, &p9u.Topen{Fid: fid, Mode: mode})
	if err != nil {
		return proto.QID{}, 0, err
	}
	// p9u.Ropen is not cached (cold compared to Rread/Rwrite); passing through
	// putCachedRMsg is a no-op for unknown types per msgcache.go.
	qid, iou := r.QID, r.IOUnit
	putCachedRMsg(r)
	return qid, iou, nil
}

// tcreate implements [Raw.Tcreate].
func (c *Conn) tcreate(ctx context.Context, fid proto.Fid, name string, perm proto.FileMode, mode uint8, extension string) (proto.QID, uint32, error) {
	if err := c.requireDialect(protocolU, "Create"); err != nil {
		return proto.QID{}, 0, err
	}
	req := &p9u.Tcreate{
		Fid:       fid,
		Name:      name,
		Perm:      perm,
		Mode:      mode,
		Extension: extension,
	}
	r, err := rtrip[*p9u.Rcreate](ctx, c, req)
	if err != nil {
		return proto.QID{}, 0, err
	}
	qid, iou := r.QID, r.IOUnit
	putCachedRMsg(r)
	return qid, iou, nil
}

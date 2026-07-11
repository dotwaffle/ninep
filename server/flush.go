package server

import (
	"context"
	"sync"

	"github.com/dotwaffle/ninep/proto"
)

// inflightMap tracks in-flight request goroutines by tag.
// It provides flush cancellation and drain-on-disconnect.
//
// The drained-counter pattern (instead of sync.WaitGroup) lets
// waitWithDeadline return on context cancellation without leaking a
// helper goroutine that parks indefinitely on wg.Wait. The drained
// channel is created lazily on first wait and closed-and-cleared when
// the inflight count returns to zero; subsequent waits allocate a new
// channel.
type inflightMap struct {
	mu      sync.Mutex
	entries map[proto.Tag]inflightEntry
	count   int
	drained chan struct{}
}

type inflightEntry struct {
	rctx *requestCtx
	// done is created lazily by flushWait and closed by finish. It lets a
	// Tflush handler wait for the flushed request's response to be written
	// before returning Rflush. Nil on the normal path, so a request that is
	// never flushed pays no extra allocation.
	done chan struct{}
	// committed is true once dispatchInline has begun writing this tag's
	// response (see commit). A committed entry is logically finished --
	// flushWait must still make a waiting Tflush block on done so Rflush
	// cannot overtake the response on the wire, but start treats a
	// committed entry as free for a legitimately reused tag instead of
	// tearing the connection down as a duplicate.
	committed bool
}

// newInflightMap returns an initialized inflight map.
func newInflightMap() *inflightMap {
	return &inflightMap{entries: make(map[proto.Tag]inflightEntry)}
}

// start registers a new in-flight request. It returns false if tag is
// already in use by a request that has not yet committed its response --
// a genuine duplicate, since the client must not reuse a tag until it has
// seen the prior response (or Rflush). If the existing entry is committed,
// its response write has already begun (see commit), so the client seeing
// it and reusing the tag is legitimate; start replaces the entry, which
// nets to no change in count, and completeCommit's identity check makes
// the original request's eventual bookkeeping call a no-op.
//
// The *requestCtx is stored so that flush can trigger cancellation
// without an additional indirection through context.CancelFunc. Caller
// must call finish(tag) or commit(tag)+completeCommit(tag) when start
// returns true and the handler goroutine completes.
func (im *inflightMap) start(tag proto.Tag, rctx *requestCtx) bool {
	im.mu.Lock()
	defer im.mu.Unlock()
	if entry, exists := im.entries[tag]; exists {
		if !entry.committed {
			return false
		}
		// A Tflush may have registered a waiter on the committed entry
		// during its response-write window. Release it before the entry is
		// replaced, or that Tflush blocks the full flush-wait deadline and
		// tears the connection down.
		if entry.done != nil {
			close(entry.done)
		}
		im.entries[tag] = inflightEntry{rctx: rctx}
		return true
	}
	im.entries[tag] = inflightEntry{rctx: rctx}
	im.count++
	return true
}

// finish removes the tag and closes its done channel, releasing any waiting
// Tflush. Use it on paths with no separate response write (resp == nil, the
// handler-panic fallback), where there is nothing to order the Tflush after.
// Must be called exactly once per start call.
func (im *inflightMap) finish(tag proto.Tag) {
	if done := im.remove(tag); done != nil {
		close(done)
	}
}

// remove drops the tag from the map and signals the drain channel if no
// requests remain, returning the entry's done channel WITHOUT closing it (nil
// if no Tflush was waiting). Used by finish on paths with no separate
// response write, where there is no wire ordering to preserve. Must be
// paired with exactly one start.
func (im *inflightMap) remove(tag proto.Tag) chan struct{} {
	im.mu.Lock()
	defer im.mu.Unlock()
	entry := im.entries[tag]
	delete(im.entries, tag)
	im.count--
	if im.count == 0 && im.drained != nil {
		close(im.drained)
		im.drained = nil
	}
	return entry.done
}

// commit marks tag's entry as committed -- its response write is about to
// begin -- without removing it from the map. dispatchInline calls commit
// BEFORE writing the response and completeCommit AFTER.
//
// Keeping the entry in the map (instead of removing it, as the old scheme
// did) matters: if it were removed here, a Tflush arriving in the window
// between this call and the write's completion would find no entry, return
// Rflush immediately, and could win the race for writeMu against the
// response it was meant to follow -- letting Rflush overtake the flushed
// response on the wire, aliasing a reused tag onto the still-in-flight
// original. flushWait still finds a committed entry during that window and
// creates a done channel for its caller to wait on; completeCommit (not
// this call) is what returns that channel, since a Tflush registering
// concurrently with the write can create it after commit has already
// returned.
func (im *inflightMap) commit(tag proto.Tag) {
	im.mu.Lock()
	defer im.mu.Unlock()
	entry := im.entries[tag]
	entry.committed = true
	im.entries[tag] = entry
}

// completeCommit finishes the bookkeeping commit started: it removes the
// entry, decrements count, signals the drain channel, and returns the
// entry's done channel WITHOUT closing it (nil if no Tflush registered a
// waiter). The caller must close the returned channel after this call
// returns. Fetching done here rather than at commit time is what makes a
// Tflush that registers while the response write is in progress visible:
// flushWait may create the channel at any point up to this call.
//
// rctx must be the same *requestCtx passed to the matching start/commit:
// if the tag's entry no longer holds that rctx, a legitimately reused tag
// (start treats a committed entry as free) has already replaced it and
// absorbed this slot's count, so completeCommit is a no-op. Must be called
// exactly once per commit.
func (im *inflightMap) completeCommit(tag proto.Tag, rctx *requestCtx) chan struct{} {
	im.mu.Lock()
	defer im.mu.Unlock()
	entry, ok := im.entries[tag]
	if !ok || entry.rctx != rctx {
		return nil
	}
	delete(im.entries, tag)
	im.count--
	if im.count == 0 && im.drained != nil {
		close(im.drained)
		im.drained = nil
	}
	return entry.done
}

// drainChan returns a channel that is closed when count reaches zero, or
// nil if count is already zero. Caller must hold im.mu.
func (im *inflightMap) drainChan() chan struct{} {
	if im.count == 0 {
		return nil
	}
	if im.drained == nil {
		im.drained = make(chan struct{})
	}
	return im.drained
}

// flushWait cancels the request with the given tag and returns a channel that
// closes when its handler finishes (after its response is written). It does
// NOT remove the entry -- the handler goroutine is still running and will call
// finish when done, which both removes the entry and closes the channel,
// preventing tag-reuse races. Returns nil if the tag is not in flight, in
// which case the caller need not wait.
func (im *inflightMap) flushWait(tag proto.Tag) <-chan struct{} {
	im.mu.Lock()
	defer im.mu.Unlock()
	entry, ok := im.entries[tag]
	if !ok {
		return nil
	}
	entry.rctx.flush(errTflushCancelled)
	if entry.done == nil {
		entry.done = make(chan struct{})
		im.entries[tag] = entry
	}
	return entry.done
}

// cancelAll cancels all in-flight request contexts. Used during connection
// cleanup. Does not remove entries; handlers still need to call finish.
func (im *inflightMap) cancelAll() {
	im.mu.Lock()
	defer im.mu.Unlock()
	for _, entry := range im.entries {
		entry.rctx.flush(errConnCleanup)
	}
}

// waitWithDeadline blocks until all in-flight handlers finish or the context
// deadline expires. Returns the context error if the deadline is exceeded.
//
// On deadline expiry no helper goroutine is left running: the drain channel
// is closed by finish() when count reaches zero, so multiple callers can
// share it and there is nothing parked on a WaitGroup.
func (im *inflightMap) waitWithDeadline(ctx context.Context) error {
	im.mu.Lock()
	ch := im.drainChan()
	im.mu.Unlock()
	if ch == nil {
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// len returns the number of in-flight entries.
func (im *inflightMap) len() int {
	im.mu.Lock()
	defer im.mu.Unlock()
	return len(im.entries)
}

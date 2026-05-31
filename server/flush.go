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
}

// newInflightMap returns an initialized inflight map.
func newInflightMap() *inflightMap {
	return &inflightMap{entries: make(map[proto.Tag]inflightEntry)}
}

// start registers a new in-flight request. It returns false if tag is already
// in use. The *requestCtx is stored so that flush can trigger cancellation
// without an additional indirection through context.CancelFunc. Caller must
// call finish(tag) when start returns true and the handler goroutine completes.
func (im *inflightMap) start(tag proto.Tag, rctx *requestCtx) bool {
	im.mu.Lock()
	defer im.mu.Unlock()
	if _, exists := im.entries[tag]; exists {
		return false
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
// if no Tflush was waiting). dispatchInline calls remove BEFORE writing the
// response, then closes the returned channel AFTER the write. Removing early
// frees the tag before the client can receive the response and reuse it (a
// reused tag arriving while the entry was still registered would be rejected
// as a duplicate and tear the connection down); deferring the close keeps a
// waiting Tflush ordered after the flushed response. Like finish, must be
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

// flush cancels the context of the request with the given tag without waiting
// for it to finish. It does NOT remove the entry -- the handler goroutine is
// still running and will call finish when done.
func (im *inflightMap) flush(tag proto.Tag) {
	im.mu.Lock()
	defer im.mu.Unlock()
	if entry, ok := im.entries[tag]; ok {
		entry.rctx.flush(errTflushCancelled)
	}
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

// wait blocks until all in-flight handler goroutines have called finish.
func (im *inflightMap) wait() {
	im.mu.Lock()
	ch := im.drainChan()
	im.mu.Unlock()
	if ch == nil {
		return
	}
	<-ch
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

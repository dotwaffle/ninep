package server

import (
	"context"
	"sync"

	"github.com/dotwaffle/ninep/proto"
)

// inflightMap tracks in-flight request goroutines by tag.
// It provides flush cancellation and drain-on-disconnect.
type inflightMap struct {
	mu      sync.Mutex
	entries map[proto.Tag]inflightEntry
	wg      sync.WaitGroup
}

type inflightEntry struct {
	rctx *requestCtx
}

// newInflightMap returns an initialized inflight map.
func newInflightMap() *inflightMap {
	return &inflightMap{entries: make(map[proto.Tag]inflightEntry)}
}

// start registers a new in-flight request. The *requestCtx is stored so
// that flush can trigger cancellation without an additional indirection
// through context.CancelFunc. Caller must call finish(tag) when the handler
// goroutine completes.
func (im *inflightMap) start(tag proto.Tag, rctx *requestCtx) {
	im.mu.Lock()
	defer im.mu.Unlock()
	im.entries[tag] = inflightEntry{rctx: rctx}
	im.wg.Add(1)
}

// finish removes the tag from the inflight map and signals the WaitGroup.
// Must be called exactly once per start call.
func (im *inflightMap) finish(tag proto.Tag) {
	im.mu.Lock()
	defer im.mu.Unlock()
	delete(im.entries, tag)
	im.wg.Done()
}

// flush cancels the context of the request with the given tag. It does NOT
// remove the entry -- the handler goroutine is still running and will call
// finish when done. This prevents tag-reuse races.
func (im *inflightMap) flush(tag proto.Tag) {
	im.mu.Lock()
	defer im.mu.Unlock()
	if entry, ok := im.entries[tag]; ok {
		entry.rctx.flush(errTflushCancelled)
	}
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
	im.wg.Wait()
}

// waitWithDeadline blocks until all in-flight handlers finish or the context
// deadline expires. Returns the context error if the deadline is exceeded.
//
// Note: the goroutine spawned here will leak if wg never reaches zero and the
// deadline expires. This is acceptable in the cleanup and re-negotiation paths
// because the connection is being torn down and the goroutine will exit once
// all handler goroutines eventually finish and call wg.Done.
func (im *inflightMap) waitWithDeadline(ctx context.Context) error {
	ch := make(chan struct{})
	go func() {
		im.wg.Wait()
		close(ch)
	}()

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

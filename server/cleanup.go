package server

import (
	"context"
	"log/slog"
	"time"
)

// cleanupDeadline is the maximum time to wait for inflight requests to drain
// during connection cleanup.
const cleanupDeadline = 5 * time.Second

// cleanup performs orderly connection shutdown:
//  1. Cancel all inflight request contexts.
//  2. Wait for inflight handlers to finish (with deadline).
//  3. Close the work channel and wait for workers to exit.
//  4. Clunk all fids.
//  5. Close the responses channel (terminates writer goroutine).
func (c *conn) cleanup() {
	// Step 1: Cancel all inflight requests.
	c.inflight.cancelAll()

	// Step 2: Wait for handlers to finish with deadline.
	deadlineCtx, deadlineCancel := context.WithTimeout(context.Background(), cleanupDeadline)
	defer deadlineCancel()

	if err := c.inflight.waitWithDeadline(deadlineCtx); err != nil {
		c.logger.Warn("cleanup: timed out waiting for inflight requests",
			slog.Int("remaining", c.inflight.len()),
		)
	}

	// Step 3: Close the work channel and wait for workers to exit, but
	// only up to the cleanup deadline. If a handler is stuck ignoring
	// context cancellation, we must not hang cleanup — the old
	// goroutine-per-request model also orphaned stuck handlers after
	// the deadline (they remained until the process exited or they
	// eventually returned). Same semantics here.
	//
	// readLoop has already returned (serve calls cleanup after it), so
	// no further sends to workCh are possible. Idle workers see the
	// closed channel and exit immediately; workers currently running a
	// handler exit when the handler returns.
	close(c.workCh)
	workerDone := make(chan struct{})
	go func() {
		c.workerWG.Wait()
		close(workerDone)
	}()
	select {
	case <-workerDone:
	case <-deadlineCtx.Done():
		c.logger.Warn("cleanup: timed out waiting for workers to exit",
			slog.Int("remaining_workers", int(c.workerCount.Load())),
		)
	}

	// Step 4: Clunk all fids and release handles.
	// Use swap-and-clear pattern: clunkAll returns all states, iterate outside lock.
	states := c.fids.clunkAll()
	if len(states) > 0 {
		c.otelInst.recordFidChange(-int64(len(states)))
	}
	for _, fs := range states {
		releaseHandle(context.Background(), fs, c.logger)
		if closer, ok := fs.node.(NodeCloser); ok {
			if err := closer.Close(context.Background()); err != nil {
				c.logger.Debug("node close error during cleanup", slog.Any("error", err))
			}
		}
	}
	if len(states) > 0 {
		c.logger.Debug("cleanup: clunked fids",
			slog.Int("count", len(states)),
		)
	}

	// Step 5: Close responses channel to terminate writer goroutine.
	close(c.responses)
}

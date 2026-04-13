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
//  3. Clunk all fids.
//  4. Close the responses channel (terminates writer goroutine).
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

	// Step 3: Clunk all fids and release handles.
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

	// Step 4: Close responses channel to terminate writer goroutine.
	close(c.responses)
}

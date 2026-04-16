package server

import (
	"context"
	"log/slog"
	"time"
)

// cleanupDeadline is the maximum time to wait for inflight requests to drain
// during connection cleanup.
const cleanupDeadline = 5 * time.Second

// cleanup performs orderly connection shutdown for the recv-mutex worker
// model:
//
//  1. Cancel all inflight request contexts.
//  2. Wait for inflight handlers to drain (with deadline).
//  3. Close net.Conn so the recvMu-holder's read errors out.
//  4. Wait for handleRequest goroutines to exit (bounded by deadline).
//  5. Clunk all fids.
//
// Each handleRequest goroutine encodes and writev's its response inline
// from sendResponseInline under writeMu, so there is no separate writer
// goroutine or response channel to drain on shutdown.
func (c *conn) cleanup() {
	// Step 1: Cancel all inflight requests so handlers respecting
	// ctx.Done() return promptly.
	c.inflight.cancelAll()

	// Step 2: Wait for handlers to finish with deadline. If a handler
	// ignores ctx.Done() (e.g. a stuck syscall), we log and move on -- the
	// same contract as before (TestDisconnectCleanup_DrainDeadline).
	deadlineCtx, deadlineCancel := context.WithTimeout(context.Background(), cleanupDeadline)
	defer deadlineCancel()

	if err := c.inflight.waitWithDeadline(deadlineCtx); err != nil {
		c.logger.Warn("cleanup: timed out waiting for inflight requests",
			slog.Int("remaining", c.inflight.len()),
		)
	}

	// Step 3: Close net.Conn so the recvMu-holder's read errors out and
	// exits. Goroutines parked on recvMu.Lock() observe recvShutdown on
	// acquire and exit. Idempotent: if the watcher goroutine in serve
	// already closed nc on ctx.Done, this returns ErrClosed (ignored).
	_ = c.nc.Close()

	// Wait for handleRequest goroutines to exit, bounded by the cleanup
	// deadline. A stuck handler would already have caused step 2 to log;
	// this step waits for the loop bodies to fall through. Same orphan
	// semantics as before: stuck handlers remain until they eventually
	// return.
	recvDone := make(chan struct{})
	go func() {
		c.recvWG.Wait()
		close(recvDone)
	}()
	select {
	case <-recvDone:
	case <-deadlineCtx.Done():
		c.logger.Warn("cleanup: timed out waiting for recv goroutines to exit",
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
}

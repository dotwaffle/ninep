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

	drained := c.inflight.waitWithDeadline(deadlineCtx) == nil
	if !drained {
		c.logger.Warn("cleanup: timed out waiting for inflight requests",
			slog.Int("remaining", c.inflight.len()),
		)
		c.otelInst.recordAbnormalEvent(reasonDrainTimeout)
	}

	// Step 3: Close net.Conn so the recvMu-holder's read errors out and
	// exits. Goroutines parked on recvMu.Lock() observe recvShutdown on
	// acquire and exit. Idempotent: if the watcher goroutine in serve
	// already closed nc on ctx.Done, this returns ErrClosed (ignored).
	_ = c.nc.Close()

	// Wait for handleRequest goroutines to exit, but only when handlers
	// drained. With nc closed, the read goroutines fall through promptly, so
	// recvWG.Wait completes. When a handler is permanently stuck (e.g. a hung
	// syscall ignoring ctx), recvWG never reaches zero; spawning a waiter then
	// would leak it forever alongside the stuck handler, so we skip the wait.
	if drained {
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
	}

	// Step 4: Clunk all fids and release handles.
	// Use swap-and-clear pattern: clunkAll returns all states, iterate outside lock.
	states := c.fids.clunkAll()
	if len(states) > 0 {
		c.otelInst.recordFidChange(-int64(len(states)))
	}
	for _, fs := range states {
		// decRefNode unconditionally: the fid is gone from the table either
		// way, so it no longer references its node regardless of whether the
		// handle/node release below also runs. Its return value also gates
		// NodeCloser.Close below: a walk-clone or xattrwalk alias of this
		// fid's node may still be live on another fid in this same batch.
		lastRef := decRefNode(fs.currentNode())
		// Release handles and close nodes only when handlers drained. A stuck
		// handler may still be reading through fs.handle; closing its fd here
		// would race that read (use-after-close on an fd that could be
		// reused). Leaving it leaks the fd with the stuck handler, which is
		// the safe trade.
		if !drained {
			continue
		}
		fs.releaseNow(context.Background(), c.logger, lastRef)
	}
	if len(states) > 0 {
		c.logger.Debug("cleanup: clunked fids",
			slog.Int("count", len(states)),
		)
	}
}

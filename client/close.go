package client

import (
	"context"
	"log/slog"
	"time"
)

// cleanupDeadline is the default drain window for Close(). Mirrors
// server/cleanup.go:11 exactly — the asymmetry (client 1 MiB msize, server
// 4 MiB) does not apply here; drain deadline is a liveness property, not a
// throughput property. Per D-22 (.planning/phases/19/19-CONTEXT.md).
const cleanupDeadline = 5 * time.Second

// Close initiates an orderly shutdown with a default 5-second drain
// deadline per D-22. Semantics:
//
//  1. Signal shutdown (close closeCh, close nc, cancel all inflight).
//     This unblocks callers parked on respCh (cancelAll closes every
//     respCh), tagAllocator.acquire (closeCh select arm), and readLoop
//     (nc.Close surfaces as a read error).
//  2. Wait up to 5s for caller goroutines (callerWG) to return.
//  3. Wait for the read goroutine (readerWG) to exit — unbounded because
//     step 1 closed nc which will surface as a read error immediately.
//
// Close does NOT return while caller or read goroutines are still running
// (D-24 — no background reaping). Safe to call from multiple goroutines;
// idempotent via closeOnce.
//
// Close returns nil even if the drain deadline fires — the deadline is a
// log-and-proceed signal, not an error condition (consumers have no
// recovery path from "server wedged our goroutine").
func (c *Conn) Close() error {
	return c.shutdown(context.Background(), cleanupDeadline)
}

// Shutdown is Close with a caller-supplied context for custom timeout
// behavior (D-23). If ctx has a Deadline, the remaining time is used as
// the drain bound; otherwise the default 5s applies. Named after
// http.Server.Shutdown for idiom parity.
//
// An already-cancelled ctx (ctx.Err() != nil) skips the drain but still
// waits for the read goroutine to exit, preserving the D-24 invariant
// that no goroutines leak past Shutdown's return.
func (c *Conn) Shutdown(ctx context.Context) error {
	timeout := cleanupDeadline
	if deadline, ok := ctx.Deadline(); ok {
		if d := time.Until(deadline); d > 0 {
			timeout = d
		} else {
			timeout = 0 // already past deadline — no drain window
		}
	}
	return c.shutdown(ctx, timeout)
}

// shutdown is the shared implementation of Close + Shutdown. Idempotent
// via closeOnce in signalShutdown; concurrent callers converge on the
// same callerWG.Wait / readerWG.Wait. Per D-24 readerWG.Wait runs
// unconditionally before return — no background reaping.
func (c *Conn) shutdown(ctx context.Context, timeout time.Duration) error {
	c.signalShutdown() // idempotent — fires only on first call (closeOnce)

	// Drain callers (callerWG) bounded by timeout OR ctx.Done.
	callerDone := make(chan struct{})
	go func() {
		c.callerWG.Wait()
		close(callerDone)
	}()

	var drainTimer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		drainTimer = t.C
	} else {
		drainTimer = closedTimeChan()
	}

	select {
	case <-callerDone:
		// Clean drain.
	case <-drainTimer:
		c.logger.Warn("client.Conn shutdown: caller drain timed out",
			slog.Int("remaining_inflight", c.inflight.len()),
			slog.Duration("timeout", timeout),
		)
	case <-ctx.Done():
		// ctx.Done fires even when no deadline was set (explicit cancel).
		// Both paths fall through to readerWG.Wait.
		c.logger.Warn("client.Conn shutdown: ctx cancelled during drain",
			slog.Any("error", ctx.Err()),
			slog.Int("remaining_inflight", c.inflight.len()),
		)
	}

	// Always wait on the read goroutine. It exits as soon as nc.Close
	// surfaces in its Read call; no timeout applied here because a stuck
	// read goroutine indicates a real bug worth hanging the test.
	//
	// Per D-24: do not return to the caller while reaping in the
	// background — this Wait is the load-bearing invariant of Close.
	c.readerWG.Wait()
	return nil
}

// closedTimeChan returns a channel that's already closed. Used when
// timeout == 0 (ctx already past deadline) so the select-on-drainTimer
// case fires immediately without the caller having to special-case the
// zero-timeout path.
func closedTimeChan() <-chan time.Time {
	ch := make(chan time.Time)
	close(ch)
	return ch
}

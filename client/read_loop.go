package client

import (
	"errors"
	"io"
	"net"
)

// readLoop is a minimal placeholder that Task 3 replaces with the full
// wire.ReadSize + wire.ReadBody + newRMessage factory + inflight.deliver
// loop. During Task 2 it exists only so Dial can spawn a goroutine and
// TestDial_SpawnsReadGoroutine observes the NumGoroutine bump. It exits
// either when the net.Conn is closed (peer hangup or Close) or when
// signalShutdown fires.
//
// TODO(plan-19-03-task-3): replace with real R-message dispatch loop.
func (c *Conn) readLoop() {
	defer c.readerWG.Done()

	// Block on the net.Conn. On Close(), net.Pipe's reader returns
	// io.ErrClosedPipe (wrapped as net.ErrClosed on TCP/unix). Either way
	// we exit; Task 3 replaces with the real dispatch loop.
	var buf [1]byte
	for {
		_, err := c.nc.Read(buf[:])
		if err != nil {
			// Normal shutdown paths — drop quietly.
			if !isClosedErr(err) {
				c.logger.Debug("client: readLoop exit")
			}
			c.signalShutdown()
			return
		}
		// Any bytes read during the Task 2 placeholder are ignored; Task 3's
		// real readLoop parses them as R-message frames.
	}
}

// signalShutdown is safe to call multiple times (closeOnce). Closes closeCh,
// cancels all inflight callers, and closes nc so any peer blocked on read
// also exits. The full Close/Shutdown drain sequence lives in Plan 19-05;
// this helper just fires the signals.
func (c *Conn) signalShutdown() {
	c.closeOnce.Do(func() {
		close(c.closeCh)
		_ = c.nc.Close()
		c.inflight.cancelAll()
	})
}

// isClosedErr reports whether err is from a closed net.Conn or an EOF path.
// Prevents spurious log lines during graceful shutdown.
func isClosedErr(err error) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.ErrClosedPipe)
}

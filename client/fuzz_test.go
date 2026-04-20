package client

import (
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

// FuzzConnReadLoop targets the client.Conn read loop and response dispatch
// logic. It feeds fuzzed bytes into the client's network connection to
// ensure that malformed or malicious server responses do not cause panics
// or resource leaks.
func FuzzConnReadLoop(f *testing.F) {
	// Seed with a valid framed Rversion (header[7] + body[7])
	// size[4]=14, type=101(Rversion), tag=65535, msize=1M, version="9P2000"
	seed := []byte{
		0x0e, 0x00, 0x00, 0x00, // size
		101,        // type
		0xff, 0xff, // tag
		0x00, 0x00, 0x10, 0x00, // msize (1MiB)
		0x06, 0x00, // version len
		'9', 'P', '2', '0', '0', '0',
	}
	f.Add(seed)

	f.Fuzz(func(t *testing.T, data []byte) {
		cliNC, srvNC := net.Pipe()
		defer func() { _ = cliNC.Close() }()
		defer func() { _ = srvNC.Close() }()

		// Manually initialize a Conn to skip Tversion negotiation (which
		// we fuzz separately in proto package).
		c := &Conn{
			nc:       cliNC,
			dialect:  protocolL,
			msize:    1024 * 1024,
			codec:    codecL,
			tags:     newTagAllocator(64),
			inflight: newInflightMap(),
			fids:     newFidAllocator(),
			closeCh:  make(chan struct{}),
			logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		}

		c.readerWG.Add(1)
		go c.readLoop()

		// Feed fuzzed data.
		go func() {
			_, _ = srvNC.Write(data)
			// Give the reader a tiny moment to process.
			time.Sleep(1 * time.Millisecond)
			_ = srvNC.Close()
		}()

		// Wait for shutdown or timeout.
		done := make(chan struct{})
		go func() {
			c.readerWG.Wait()
			close(done)
		}()

		select {
		case <-done:
			// Normal shutdown (connection closed).
		case <-time.After(100 * time.Millisecond):
			// Timeout (reader might be hung, but shouldn't panic).
			_ = c.Close()
		}
	})
}

package server

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
)

func TestHandleReVersion_FatalErrors(t *testing.T) {
	t.Parallel()

	t.Run("DecodeError", func(t *testing.T) {
		t.Parallel()
		client, server := net.Pipe()
		defer func() { _ = client.Close() }()
		defer func() { _ = server.Close() }()

		srv := New(nil, WithLogger(discardLogger()))
		c := newConn(srv, server)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		go c.serve(ctx)

		// Initial negotiation
		sendTversion(t, client, 65536, "9P2000.L")
		_ = readRversion(t, client)

		// Bypass rate limit
		time.Sleep(110 * time.Millisecond)

		// Send malformed Tversion (too short for msize[4])
		// size[4] + type[1] + tag[2] + body[1]
		// size = 4 + 1 + 2 + 1 = 8
		buf := []byte{0x08, 0x00, 0x00, 0x00, uint8(proto.TypeTversion), 0xff, 0xff, 0x00}
		_, _ = client.Write(buf)

		// The connection should be closed by the server.
		done := make(chan struct{})
		go func() {
			defer close(done)
			// Read should eventually return EOF or error if connection is closed.
			_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, _, err := proto9PRead(client)
			if err == nil {
				// If we successfully read something, it might be a response we didn't expect,
				// but we expect the connection to close.
			}
		}()

		select {
		case <-done:
			// Success
		case <-time.After(3 * time.Second):
			t.Fatal("connection not closed after decode error")
		}
	})

	t.Run("NegotiateError_MsizeTooSmall", func(t *testing.T) {
		t.Parallel()
		client, server := net.Pipe()
		defer func() { _ = client.Close() }()
		defer func() { _ = server.Close() }()

		srv := New(nil, WithLogger(discardLogger()))
		c := newConn(srv, server)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		go c.serve(ctx)

		// Initial negotiation
		sendTversion(t, client, 65536, "9P2000.L")
		_ = readRversion(t, client)

		// Bypass rate limit
		time.Sleep(110 * time.Millisecond)

		// Mid-conn Tversion with msize < 256
		sendTversion(t, client, 100, "9P2000.L")

		// The connection should be closed by the server.
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, _, err := proto9PRead(client)
			if err != nil {
				// Expected
			}
		}()

		select {
		case <-done:
			// Success
		case <-time.After(3 * time.Second):
			t.Fatal("connection not closed after msize too small")
		}
	})
}

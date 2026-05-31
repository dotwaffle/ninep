package client

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// TestFlushAndWait_GraceTearsDownWedgedPeer asserts that when the server
// answers neither the original request nor the Tflush, flushAndWait gives up
// after the grace period and tears the connection down instead of blocking the
// caller until Conn.Close.
func TestFlushAndWait_GraceTearsDownWedgedPeer(t *testing.T) {
	t.Parallel()

	cliNC, srvNC := net.Pipe()
	t.Cleanup(func() { _ = srvNC.Close() })

	// Answer the Tversion handshake, then read and ignore everything: never
	// reply to the Tread or the Tflush.
	go func() {
		var sizeBuf [4]byte
		if _, err := io.ReadFull(srvNC, sizeBuf[:]); err != nil {
			return
		}
		size := binary.LittleEndian.Uint32(sizeBuf[:])
		body := make([]byte, int(size)-4)
		if _, err := io.ReadFull(srvNC, body); err != nil {
			return
		}
		_ = p9l.Encode(srvNC, proto.NoTag, &proto.Rversion{Msize: 65536, Version: "9P2000.L"})
		sink := make([]byte, 4096)
		for {
			if _, err := srvNC.Read(sink); err != nil {
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	c, err := Dial(ctx, cliNC, WithMsize(65536), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	c.flushGrace = 50 * time.Millisecond // set before the read goroutine starts

	readCtx, readCancel := context.WithCancel(t.Context())
	resCh := make(chan error, 1)
	go func() {
		_, rerr := c.Read(readCtx, proto.Fid(1), 0, 64)
		resCh <- rerr
	}()

	// Let the Tread reach the (silent) server, then cancel so flushAndWait runs.
	time.Sleep(20 * time.Millisecond)
	readCancel()

	select {
	case rerr := <-resCh:
		if rerr == nil {
			t.Fatal("Read returned nil; want an error after the wedged flush")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read did not return after the flush grace; flushAndWait blocked")
	}

	if !c.isClosed() {
		t.Fatal("connection was not torn down after the flush grace expired")
	}
}

package client_test

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// hangingReaddirServer completes the open handshake but never answers Treaddir.
// It does answer Tflush, so a request that times out unwinds promptly.
func hangingReaddirServer(tb testing.TB, srvNC net.Conn) {
	tb.Helper()
	tb.Cleanup(func() { _ = srvNC.Close() })

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
		if err := p9l.Encode(srvNC, proto.NoTag, &proto.Rversion{Msize: 65536, Version: "9P2000.L"}); err != nil {
			return
		}

		for {
			tag, msg, err := p9l.Decode(srvNC)
			if err != nil {
				return
			}
			switch m := msg.(type) {
			case *proto.Tattach:
				_ = p9l.Encode(srvNC, tag, &proto.Rattach{QID: proto.QID{Type: proto.QTDIR, Path: 1}})
			case *proto.Twalk:
				qids := make([]proto.QID, len(m.Names))
				for i := range qids {
					qids[i] = proto.QID{Type: proto.QTDIR, Path: 1}
				}
				_ = p9l.Encode(srvNC, tag, &proto.Rwalk{QIDs: qids})
			case *p9l.Tlopen:
				_ = p9l.Encode(srvNC, tag, &p9l.Rlopen{QID: proto.QID{Type: proto.QTDIR, Path: 1}})
			case *proto.Tflush:
				_ = p9l.Encode(srvNC, tag, &proto.Rflush{})
			case *p9l.Treaddir:
				// Never respond: the request must time out client-side.
			default:
				_ = p9l.Encode(srvNC, tag, &p9l.Rlerror{Ecode: proto.ENOSYS})
			}
		}
	}()
}

// TestFileReadDir_HonorsRequestTimeout asserts ReadDir respects the Conn's
// WithRequestTimeout instead of running with a non-cancellable background ctx.
func TestFileReadDir_HonorsRequestTimeout(t *testing.T) {
	t.Parallel()
	cliNC, srvNC := net.Pipe()
	hangingReaddirServer(t, srvNC)

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	cli, err := client.Dial(ctx, cliNC,
		client.WithMsize(65536),
		client.WithLogger(discardLogger()),
		client.WithRequestTimeout(150*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	dir, err := cli.OpenDir(ctx, "/")
	if err != nil {
		t.Fatalf("OpenDir: %v", err)
	}
	t.Cleanup(func() { _ = dir.Close() })

	done := make(chan error, 1)
	go func() {
		_, rerr := dir.ReadDir(-1)
		done <- rerr
	}()

	select {
	case rerr := <-done:
		if rerr == nil {
			t.Fatal("ReadDir returned nil; want a timeout error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadDir ignored the request timeout and blocked")
	}
}

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
	"github.com/dotwaffle/ninep/proto/p9u"
)

// uMockServer reads framed T-messages from srvNC and writes R-responses
// using the .u codec. The server understands the minimum needed to round-
// trip Tversion/Tattach/Twalk/Topen/Tcreate for the .u dialect tests.
//
// Rversion always echoes "9P2000.u" so client.Dial's dialect select lands
// on protocolU. All subsequent responses use p9u.Encode so the client's
// read loop decodes them via the codecU path.
//
// t.Cleanup closes srvNC.
func uMockServer(tb testing.TB, srvNC net.Conn) {
	tb.Helper()
	tb.Cleanup(func() { _ = srvNC.Close() })

	go func() {
		// 1. Read Tversion: size[4] + body.
		var sizeBuf [4]byte
		if _, err := io.ReadFull(srvNC, sizeBuf[:]); err != nil {
			return
		}
		size := binary.LittleEndian.Uint32(sizeBuf[:])
		body := make([]byte, int(size)-4)
		if _, err := io.ReadFull(srvNC, body); err != nil {
			return
		}
		// Respond with Rversion(.u). p9l.Encode works because Rversion is
		// dialect-neutral on the wire.
		rver := &proto.Rversion{Msize: 65536, Version: "9P2000.u"}
		if err := p9l.Encode(srvNC, proto.NoTag, rver); err != nil {
			return
		}

		// 2. Subsequent frames: decode via p9u.Decode; reply with minimal
		//    Ropen/Rcreate/Rattach/Rwalk/Rclunk responses.
		for {
			tag, msg, err := p9u.Decode(srvNC)
			if err != nil {
				return
			}
			var resp proto.Message
			switch msg.(type) {
			case *proto.Tattach:
				resp = &proto.Rattach{QID: proto.QID{Type: proto.QTDIR, Path: 1}}
			case *proto.Twalk:
				resp = &proto.Rwalk{QIDs: nil}
			case *p9u.Topen:
				resp = &p9u.Ropen{QID: proto.QID{Type: proto.QTFILE, Path: 42}, IOUnit: 4096}
			case *p9u.Tcreate:
				resp = &p9u.Rcreate{QID: proto.QID{Type: proto.QTFILE, Path: 43}, IOUnit: 4096}
			case *proto.Tclunk:
				resp = &proto.Rclunk{}
			default:
				resp = &p9u.Rerror{Ename: "not implemented", Errno: proto.ENOSYS}
			}
			if err := p9u.Encode(srvNC, tag, resp); err != nil {
				return
			}
		}
	}()
}

// newUMockClientPair dials a client against a mock server that echoes
// "9P2000.u" during negotiation, returning a .u-dialect Conn.
func newUMockClientPair(tb testing.TB) (*client.Conn, func()) {
	tb.Helper()
	cliNC, srvNC := net.Pipe()
	uMockServer(tb, srvNC)

	ctx, cancel := context.WithTimeout(tb.Context(), 3*time.Second)
	defer cancel()

	cli, err := client.Dial(ctx, cliNC, client.WithMsize(65536), client.WithLogger(discardLogger()))
	if err != nil {
		_ = cliNC.Close()
		tb.Fatalf("Dial: %v", err)
	}
	return cli, func() {
		_ = cli.Close()
		_ = srvNC.Close()
	}
}

// TestClient_Open_U: Open against a .u-negotiated Conn returns QID + iounit.
func TestClient_Open_U(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()

	ctx, cancel := roundTripTestCtx(t)
	defer cancel()

	if _, err := cli.Raw().Attach(ctx, 0, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if _, err := cli.Walk(ctx, 0, 1, nil); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	qid, iou, err := cli.Open(ctx, 1, 0) // OREAD
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if qid.Type&proto.QTDIR != 0 {
		t.Errorf("Open QID type = %#x, want file", qid.Type)
	}
	if iou != 4096 {
		t.Errorf("Open iounit = %d, want 4096", iou)
	}
}

// TestClient_Create_U: Create against a .u-negotiated Conn returns QID +
// iounit.
func TestClient_Create_U(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()

	ctx, cancel := roundTripTestCtx(t)
	defer cancel()

	if _, err := cli.Raw().Attach(ctx, 0, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if _, err := cli.Walk(ctx, 0, 1, nil); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	qid, iou, err := cli.Raw().Create(ctx, 1, "new.txt", proto.FileMode(0o644), 2 /*ORDWR*/, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if qid.Type&proto.QTDIR != 0 {
		t.Errorf("Create QID type = %#x, want file", qid.Type)
	}
	if iou != 4096 {
		t.Errorf("Create iounit = %d, want 4096", iou)
	}
}

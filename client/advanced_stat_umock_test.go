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

// wantStat is the canned p9u.Stat returned by uMockStatServer for any
// incoming Tstat. File.Stat on a .u Conn returns this Stat verbatim
// (r.Stat field of Rstat).
var wantStat = p9u.Stat{
	Type:      0,
	Dev:       0,
	QID:       proto.QID{Type: proto.QTFILE, Version: 1, Path: 99},
	Mode:      proto.FileMode(0o644),
	Atime:     100,
	Mtime:     200,
	Length:    12,
	Name:      "hello.txt",
	UID:       "1000",
	GID:       "1000",
	MUID:      "",
	Extension: "",
	NUid:      1000,
	NGid:      1000,
	NMuid:     0,
}

// uMockStatServer reads Tversion then loops over p9u-coded T-messages,
// answering Tstat with an Rstat carrying the supplied stat. All other
// T-messages receive an Rerror(ENOSYS). Used to exercise File.Stat's .u
// branch in a hermetic setting (the real server does not currently
// dispatch p9u.Tstat, per dispatch.go).
func uMockStatServer(tb testing.TB, srvNC net.Conn, stat p9u.Stat) {
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
		// Advertise 9P2000.u so the client selects protocolU.
		rver := &proto.Rversion{Msize: 65536, Version: "9P2000.u"}
		if err := p9l.Encode(srvNC, proto.NoTag, rver); err != nil {
			return
		}

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
			case *p9u.Tstat:
				resp = &p9u.Rstat{Stat: stat}
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

// newUMockStatClientPair boots a u-dialect client against a mock server
// that echoes "9P2000.u" during Tversion and answers Tstat with the
// supplied stat.
func newUMockStatClientPair(tb testing.TB, stat p9u.Stat) (*client.Conn, func()) {
	tb.Helper()
	cliNC, srvNC := net.Pipe()
	uMockStatServer(tb, srvNC, stat)

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

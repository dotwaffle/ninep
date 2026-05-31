package client_test

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// packDirent encodes one packed 9P2000.L directory entry with the given resume
// offset cursor (mirrors server/dirent.go EncodeDirents for a single entry).
func packDirent(name string, offset uint64) []byte {
	buf := make([]byte, 0, 13+8+1+2+len(name))
	buf = append(buf, byte(proto.QTFILE))               // QID type
	buf = binary.LittleEndian.AppendUint32(buf, 0)      // QID version
	buf = binary.LittleEndian.AppendUint64(buf, 42)     // QID path
	buf = binary.LittleEndian.AppendUint64(buf, offset) // resume cursor
	buf = append(buf, proto.DT_REG)                     // dirent type byte
	buf = binary.LittleEndian.AppendUint16(buf, uint16(len(name)))
	buf = append(buf, name...)
	return buf
}

// stuckReaddirServer answers the readdir handshake and returns a single dirent
// with a CONSTANT offset for every Treaddir, simulating a server whose resume
// cursor never advances.
func stuckReaddirServer(tb testing.TB, srvNC net.Conn) {
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
			var resp proto.Message
			switch m := msg.(type) {
			case *proto.Tattach:
				resp = &proto.Rattach{QID: proto.QID{Type: proto.QTDIR, Path: 1}}
			case *proto.Twalk:
				qids := make([]proto.QID, len(m.Names))
				for i := range qids {
					qids[i] = proto.QID{Type: proto.QTDIR, Path: 1}
				}
				resp = &proto.Rwalk{QIDs: qids}
			case *p9l.Tlopen:
				resp = &p9l.Rlopen{QID: proto.QID{Type: proto.QTDIR, Path: 1}}
			case *p9l.Treaddir:
				// Always the same entry with a fixed cursor: never advances.
				resp = &p9l.Rreaddir{Data: packDirent("stuck", 1)}
			case *proto.Tclunk:
				resp = &proto.Rclunk{}
			default:
				resp = &p9l.Rlerror{Ecode: proto.ENOSYS}
			}
			if err := p9l.Encode(srvNC, tag, resp); err != nil {
				return
			}
		}
	}()
}

// TestFileReadDir_RejectsNonAdvancingCursor asserts ReadDir(-1) terminates with
// an error rather than looping forever (accumulating duplicates without bound)
// when the server's resume cursor never advances.
func TestFileReadDir_RejectsNonAdvancingCursor(t *testing.T) {
	t.Parallel()
	cliNC, srvNC := net.Pipe()
	stuckReaddirServer(t, srvNC)

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	cli, err := client.Dial(ctx, cliNC, client.WithMsize(65536), client.WithLogger(discardLogger()))
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

	if _, err := dir.ReadDir(-1); err == nil {
		t.Fatal("ReadDir(-1) returned nil on a non-advancing server cursor")
	} else if !strings.Contains(err.Error(), "did not advance") {
		t.Fatalf("error = %v, want a cursor-did-not-advance error", err)
	}
}

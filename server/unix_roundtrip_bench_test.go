package server

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// BenchmarkUnixRoundTrip mirrors BenchmarkRoundTrip but over a real unix-domain
// socket pair instead of net.Pipe. The unix transport supports writev() so the
// recv-mutex worker model's inline writes can coalesce response framing into a
// single syscall, where net.Pipe would split it.
//
// Ad-hoc benchmark: not part of the default test corpus. Run explicitly:
//
//	go test -run='^$' -bench='BenchmarkUnixRoundTrip' -benchmem ./server/
//
// Pingpong pattern (single in-flight) is the small-file workload Q reported as
// the regression target. The win, if any, should appear here.
func BenchmarkUnixRoundTrip(b *testing.B) {
	cases := []struct {
		name string
		send func(tb testing.TB) []byte
	}{
		{
			name: "op=getattr",
			send: func(tb testing.TB) []byte {
				return mustEncode(tb, proto.Tag(1), &p9l.Tgetattr{
					Fid:         0,
					RequestMask: proto.AttrAll,
				})
			},
		},
		{
			name: "op=read_4k",
			send: func(tb testing.TB) []byte {
				return mustEncode(tb, proto.Tag(1), &proto.Tread{
					Fid:    0,
					Offset: 0,
					Count:  4096,
				})
			},
		},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			cp := newUnixConnPair(b)
			b.Cleanup(func() { cp.close(b) })

			benchAttachFid0(b, cp)

			frame := tc.send(b)
			b.ReportAllocs()
			b.SetBytes(int64(len(frame)))
			for b.Loop() {
				if _, err := cp.client.Write(frame); err != nil {
					b.Fatalf("write: %v", err)
				}
				if err := drainResponse(cp.client); err != nil {
					b.Fatalf("drain: %v", err)
				}
			}
		})
	}
}

// newUnixConnPair returns a *connPair backed by a real connected pair of
// unix-domain sockets. Mirrors newConnPair (net.Pipe variant) closely so the
// surrounding helpers (mustEncode, benchAttachFid0, drainResponse) work.
func newUnixConnPair(tb testing.TB) *connPair {
	tb.Helper()

	root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})
	srv := New(root, WithMaxMsize(65536), WithLogger(discardLogger()))

	dir := tb.TempDir()
	sockPath := filepath.Join(dir, "ninep.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		tb.Fatalf("listen unix: %v", err)
	}

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			accepted <- nil
			return
		}
		accepted <- c
	}()

	client, err := net.Dial("unix", sockPath)
	if err != nil {
		_ = ln.Close()
		tb.Fatalf("dial unix: %v", err)
	}

	server := <-accepted
	if server == nil {
		_ = client.Close()
		_ = ln.Close()
		tb.Fatal("accept returned nil")
	}

	ctx, cancel := context.WithTimeout(tb.Context(), 30*time.Second)

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.ServeConn(ctx, server)
	}()

	tb.Cleanup(func() {
		_ = ln.Close()
		_ = os.Remove(sockPath)
	})

	// Negotiate version (mirrors newConnPair).
	sendTversion(tb, client, 65536, "9P2000.L")
	rv := readRversion(tb, client)
	if rv.Version != "9P2000.L" {
		_ = client.Close()
		cancel()
		<-done
		tb.Fatalf("version negotiation failed: got %q", rv.Version)
	}

	return &connPair{client: client, done: done, cancel: cancel}
}

// Compile-time guard that the helper file's bytes import isn't dropped if I
// later use it; keep this no-op to satisfy lint when the file evolves.
var _ = io.Discard

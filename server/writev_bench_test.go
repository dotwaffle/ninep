package server

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// writev_bench_test.go isolates the write-path syscall cost: two sequential
// nc.Write calls (header then body) vs a single net.Buffers.WriteTo that
// hits writev() on sockets which implement it (TCP, unix-domain). This
// lets us measure the actual impact of the net.Buffers change in isolation
// from the rest of the server dispatch overhead.
//
// Two transports are tested:
//   - transport=unix: real unix-domain socket (supports writev)
//   - transport=pipe: net.Pipe (does NOT support writev; falls back to
//     sequential writes)

const (
	benchHeaderSize = 7                  // size[4] + type[1] + tag[2]
	benchBodySize   = 4096 + 4           // Rread: count[4] + data[4096]
	benchTotalSize  = benchHeaderSize + benchBodySize
)

// unixPair returns a connected pair of unix-domain sockets. The returned
// conn is what a server would write responses to; the drainer runs in a
// goroutine and discards all reads to keep the writer from blocking.
func unixPair(tb testing.TB) net.Conn {
	tb.Helper()
	dir := tb.TempDir()
	sockPath := filepath.Join(dir, "ninep.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		tb.Fatalf("listen unix: %v", err)
	}
	tb.Cleanup(func() { _ = ln.Close(); _ = os.Remove(sockPath) })

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
		tb.Fatalf("dial unix: %v", err)
	}

	server := <-accepted
	if server == nil {
		_ = client.Close()
		tb.Fatal("accept returned nil")
	}

	// Drain the server side so the client's writes don't block on a full
	// socket buffer. The drainer exits when the test closes the conn.
	go func() {
		_, _ = io.Copy(io.Discard, server)
	}()

	tb.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})

	return client
}

// pipePair returns a net.Pipe pair with a drainer running on the other side.
func pipePair(tb testing.TB) net.Conn {
	tb.Helper()
	a, b := net.Pipe()
	go func() {
		_, _ = io.Copy(io.Discard, b)
	}()
	tb.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})
	return a
}

func BenchmarkWriteApproach(b *testing.B) {
	transports := []struct {
		name string
		make func(tb testing.TB) net.Conn
	}{
		{"transport=unix", unixPair},
		{"transport=pipe", pipePair},
	}

	// Third approach variant simulates the v1.1.9 Payloader pattern: the
	// fixed body + payload are separate net.Buffers entries so the payload
	// bytes go direct to socket via writev (no memcpy into a shared body
	// buffer). The caller passes the payload as body here; we split into
	// [hdr, fixedBody(4 bytes of count prefix stand-in), payload].
	approaches := []struct {
		name string
		// fn takes a conn, header, and body, and writes a full response.
		fn func(c net.Conn, hdr, body []byte) error
	}{
		{
			name: "approach=buffers_payload_split",
			fn: func() func(c net.Conn, hdr, body []byte) error {
				// Mirrors flushBatch + Payloader: [hdr, fixedBody, payload].
				// We simulate a 4-byte fixed body (Rread count prefix).
				fixedBody := make([]byte, 4)
				var arr [3][]byte
				return func(c net.Conn, hdr, body []byte) error {
					arr[0] = hdr
					arr[1] = fixedBody
					arr[2] = body // payload — NOT copied into fixedBody
					bufs := net.Buffers(arr[:])
					_, err := bufs.WriteTo(c)
					return err
				}
			}(),
		},
		{
			name: "approach=sequential",
			fn: func(c net.Conn, hdr, body []byte) error {
				if _, err := c.Write(hdr); err != nil {
					return err
				}
				if _, err := c.Write(body); err != nil {
					return err
				}
				return nil
			},
		},
		{
			name: "approach=buffers_literal",
			fn: func(c net.Conn, hdr, body []byte) error {
				bufs := net.Buffers{hdr, body}
				_, err := bufs.WriteTo(c)
				return err
			},
		},
		{
			name: "approach=buffers_reuse",
			fn: func() func(c net.Conn, hdr, body []byte) error {
				// Closure over a shared backing array to mirror the
				// production encodeResponse path that stores the backing
				// array on conn.
				var arr [2][]byte
				return func(c net.Conn, hdr, body []byte) error {
					arr[0] = hdr
					arr[1] = body
					bufs := net.Buffers(arr[:])
					_, err := bufs.WriteTo(c)
					return err
				}
			}(),
		},
	}

	for _, t := range transports {
		for _, a := range approaches {
			b.Run(t.name+"/"+a.name, func(b *testing.B) {
				conn := t.make(b)
				hdr := make([]byte, benchHeaderSize)
				body := make([]byte, benchBodySize)

				b.ReportAllocs()
				b.SetBytes(int64(benchTotalSize))
				for b.Loop() {
					if err := a.fn(conn, hdr, body); err != nil {
						b.Fatalf("write: %v", err)
					}
				}
			})
		}
	}
}

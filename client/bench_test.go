// Package client_test contains benchmarks for the client library.
//
// Mirror-exact shape of server/io_bench_test.go and server/bench_test.go
// so benchstat can diff client and server numbers on identical axes.
// Any axis-name drift breaks SC-4's v1.2.0 regression gate (per
// 24-RESEARCH.md Pitfall 3).
package client_test

import (
	"context"
	"os"
	"testing"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/client/clienttest"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

// File-wide constants — byte-identical to server/io_bench_test.go.
// Do NOT rename or reshape: benchstat groups by string match.
const (
	benchFileSize = 128 * 1024 * 1024 // 128 MiB — large enough to exercise
	numOffsets    = 1024              // offset pool depth
)

// benchFile is an in-memory file node for client-side I/O benchmarks.
// Mirrors server/io_bench_test.go:benchFile with import-path fixes.
type benchFile struct {
	server.Inode
	data []byte
}

func (f *benchFile) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return nil, 0, nil
}

func (f *benchFile) Read(_ context.Context, buf []byte, offset uint64) (int, error) {
	size := uint64(len(f.data))
	if offset >= size {
		return 0, nil
	}
	end := min(offset+uint64(len(buf)), size)
	return copy(buf, f.data[offset:end]), nil
}

func (f *benchFile) Getattr(_ context.Context, _ proto.AttrMask) (proto.Attr, error) {
	return proto.Attr{Mode: 0o644, Size: uint64(len(f.data)), NLink: 1}, nil
}

// benchDir is an empty directory node. Inode handles Lookup for children
// added via AddChild.
type benchDir struct {
	server.Inode
}

func (d *benchDir) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return nil, 0, nil
}

// newBenchTree builds a root benchDir with a 128 MiB "data" file child.
// Mirrors server/io_bench_test.go:newBenchTree shape; the data buffer is
// left zeroed (throughput benches don't inspect bytes) — the server-side
// PCG-fill is unnecessary for client measurements.
func newBenchTree(tb testing.TB) *benchDir {
	tb.Helper()
	var gen server.QIDGenerator
	dir := &benchDir{}
	dir.Init(gen.Next(proto.QTDIR), dir)
	data := make([]byte, benchFileSize)
	file := &benchFile{data: data}
	file.Init(gen.Next(proto.QTFILE), file)
	dir.AddChild("data", file.EmbeddedInode())
	return dir
}

// newBenchClient pairs a server + client via clienttest and returns the
// live *client.Conn. The harness registers tb.Cleanup to tear down both
// sides at test end. Transport is "unix" (writev-capable) or "pipe"
// (synchronous in-memory).
func newBenchClient(tb testing.TB, transport string, root server.Node, msize uint32) *client.Conn {
	tb.Helper()
	switch transport {
	case "unix":
		_, cli := clienttest.UnixPair(tb, root, clienttest.WithMsize(msize))
		return cli
	case "pipe":
		_, cli := clienttest.Pair(tb, root, clienttest.WithMsize(msize))
		return cli
	default:
		tb.Fatalf("newBenchClient: unknown transport %q", transport)
		return nil
	}
}

// benchOpenDataFile attaches, opens "data" read-only, and registers
// tb.Cleanup for the returned *File. The Conn itself is cleaned up by
// the clienttest harness.
//
// Uses Conn.OpenFile (the documented high-level surface), not the
// non-existent File.Open — same correction made in 24-01-SUMMARY.md
// Deviation 1.
func benchOpenDataFile(tb testing.TB, cli *client.Conn) *client.File {
	tb.Helper()
	if _, err := cli.Attach(tb.Context(), "bench", ""); err != nil {
		tb.Fatalf("attach: %v", err)
	}
	f, err := cli.OpenFile(tb.Context(), "data", os.O_RDONLY, 0)
	if err != nil {
		tb.Fatalf("open: %v", err)
	}
	tb.Cleanup(func() {
		_ = f.Close()
		if root := cli.Root(); root != nil {
			_ = root.Close()
		}
	})
	return f
}

// preGeneratedOffsets returns numOffsets sequential readSize-aligned
// offsets bounded by benchFileSize - readSize. Same shape as the
// sequential branch of server/io_bench_test.go:594-598.
func preGeneratedOffsets(readSize uint32) []uint64 {
	maxOffset := uint64(benchFileSize) - uint64(readSize)
	offsets := make([]uint64, numOffsets)
	for i := range offsets {
		offsets[i] = (uint64(i) * uint64(readSize)) % (maxOffset + 1)
	}
	return offsets
}

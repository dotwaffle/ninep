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

func (f *benchFile) Write(_ context.Context, data []byte, offset uint64) (uint32, error) {
	if offset >= uint64(len(f.data)) {
		return 0, nil
	}
	end := min(int(offset)+len(data), len(f.data))
	return uint32(copy(f.data[offset:end], data)), nil
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

// BenchmarkClientRead_4K measures client-side Rread throughput at 4 KiB
// reads over the default 64 KiB msize. Mirrors server/io_bench_test.go:
// BenchmarkServerRead_4K shape and axes for benchstat parity (SC-4 gate).
//
// SC-2 target (per 24-CONTEXT.md D-12): allocs/op on transport=unix is on
// par with server's BenchmarkServerRead_4K/transport=unix/encode=payloader
// (within 1 alloc/op). This baseline bench measures the current (pre
// zero-copy) path; 24-03 installs the zero-copy branch and 24-05's
// VERIFICATION.md records the absolute-allocs number.
func BenchmarkClientRead_4K(b *testing.B) {
	const readSize uint32 = 4096
	const msize uint32 = 65536

	for _, transport := range []string{"unix", "pipe"} {
		b.Run("transport="+transport, func(b *testing.B) {
			root := newBenchTree(b)
			cli := newBenchClient(b, transport, root, msize)
			f := benchOpenDataFile(b, cli)

			// dst reused across iterations (Pitfall 6). The alloc for
			// dst belongs to setup, not the per-op cost.
			dst := make([]byte, readSize)
			offsets := preGeneratedOffsets(readSize)

			b.ReportAllocs()
			b.SetBytes(int64(readSize))
			var idx int
			for b.Loop() {
				off := offsets[idx%numOffsets]
				if _, err := f.ReadAt(dst, int64(off)); err != nil {
					b.Fatalf("ReadAt: %v", err)
				}
				idx++
			}
		})
	}
}

// BenchmarkClientRead_1M measures client-side Rread throughput at 1 MiB
// reads over a negotiated 1 MiB msize. Mirrors server/io_bench_test.go:
// BenchmarkServerRead_1M shape and axes.
func BenchmarkClientRead_1M(b *testing.B) {
	const readSize uint32 = 1 << 20
	const msize uint32 = 1 << 20

	for _, transport := range []string{"unix", "pipe"} {
		b.Run("transport="+transport, func(b *testing.B) {
			root := newBenchTree(b)
			cli := newBenchClient(b, transport, root, msize)
			f := benchOpenDataFile(b, cli)

			dst := make([]byte, readSize)
			offsets := preGeneratedOffsets(readSize)

			b.ReportAllocs()
			b.SetBytes(int64(readSize))
			var idx int
			for b.Loop() {
				off := offsets[idx%numOffsets]
				if _, err := f.ReadAt(dst, int64(off)); err != nil {
					b.Fatalf("ReadAt: %v", err)
				}
				idx++
			}
		})
	}
}

func BenchmarkClientWrite_4K(b *testing.B) {
	const writeSize uint32 = 4096
	const msize uint32 = 65536

	for _, transport := range []string{"unix", "pipe"} {
		b.Run("transport="+transport, func(b *testing.B) {
			root := newBenchTree(b)
			cli := newBenchClient(b, transport, root, msize)
			f := benchOpenDataFile(b, cli)

			src := make([]byte, writeSize)
			offsets := preGeneratedOffsets(writeSize)

			b.ReportAllocs()
			b.SetBytes(int64(writeSize))
			var idx int
			for b.Loop() {
				off := offsets[idx%numOffsets]
				if _, err := f.WriteAt(src, int64(off)); err != nil {
					b.Fatalf("WriteAt: %v", err)
				}
				idx++
			}
		})
	}
}

func BenchmarkClientWrite_1M(b *testing.B) {
	const writeSize uint32 = 1 << 20
	const msize uint32 = 1 << 20

	for _, transport := range []string{"unix", "pipe"} {
		b.Run("transport="+transport, func(b *testing.B) {
			root := newBenchTree(b)
			cli := newBenchClient(b, transport, root, msize)
			f := benchOpenDataFile(b, cli)

			src := make([]byte, writeSize)
			offsets := preGeneratedOffsets(writeSize)

			b.ReportAllocs()
			b.SetBytes(int64(writeSize))
			var idx int
			for b.Loop() {
				off := offsets[idx%numOffsets]
				if _, err := f.WriteAt(src, int64(off)); err != nil {
					b.Fatalf("WriteAt: %v", err)
				}
				idx++
			}
		})
	}
}

// BenchmarkClientWalkClunk baselines the walk+clunk round-trip cost from
// the client side. Mirrors server/bench_test.go:BenchmarkWalkClunk shape
// and axes (transport={unix,pipe}).
//
// Each iteration: root.Clone(ctx) issues Twalk(rootFid, newFid, nil) —
// the 0-step "clone" walk that returns a fresh *File whose Close issues
// Tclunk. This is the client-surface equivalent of the server's raw
// Twalk(...,nil)/Tclunk frame pair. (The plan suggested File.Walk(ctx,
// nil) but that path errors out — File.Walk rejects empty names; Clone
// is the documented 0-step API per File.Walk's godoc.)
func BenchmarkClientWalkClunk(b *testing.B) {
	for _, transport := range []string{"unix", "pipe"} {
		b.Run("transport="+transport, func(b *testing.B) {
			root := newBenchTree(b)
			cli := newBenchClient(b, transport, root, 65536)

			attached, err := cli.Attach(b.Context(), "bench", "")
			if err != nil {
				b.Fatalf("attach: %v", err)
			}
			b.Cleanup(func() { _ = attached.Close() })

			b.ReportAllocs()
			// Approximate bytes on wire per iteration: Twalk(~17) +
			// Rwalk(~9) + Tclunk(~11) + Rclunk(~7) ≈ 44. SetBytes gives
			// a MB/s column that roughly tracks wire throughput; for
			// allocs/op analysis, the ReportAllocs column is the signal.
			b.SetBytes(44)
			for b.Loop() {
				clone, err := attached.Clone(b.Context())
				if err != nil {
					b.Fatalf("clone: %v", err)
				}
				if err := clone.Close(); err != nil {
					b.Fatalf("close: %v", err)
				}
			}
		})
	}
}

package server

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/internal/bufpool"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// I/O benchmarks measure read/write throughput and allocation pressure
// through the full server stack (encode → handleRequest (recvMu) →
// dispatch → bridge → sendResponseInline (writeMu, writev) → wire). Each
// subtest uses key=value naming for benchstat grouping, and all call
// b.ReportAllocs + b.SetBytes for allocs/op and MB/s columns.

// benchFile is an in-memory file node for I/O benchmarks.
type benchFile struct {
	Inode
	data []byte
}

func (f *benchFile) Open(_ context.Context, _ uint32) (FileHandle, uint32, error) {
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
	end := int(offset) + len(data)
	if end > len(f.data) {
		return 0, proto.EIO
	}
	copy(f.data[offset:], data)
	return uint32(len(data)), nil
}

func (f *benchFile) Getattr(_ context.Context, _ proto.AttrMask) (proto.Attr, error) {
	return proto.Attr{Mode: 0o644, Size: uint64(len(f.data)), NLink: 1}, nil
}

// benchDir is a directory node for I/O benchmarks. It relies on Inode's
// built-in Lookup for child resolution via the tree.
type benchDir struct {
	Inode
}

func (d *benchDir) Open(_ context.Context, _ uint32) (FileHandle, uint32, error) {
	return nil, 0, nil
}

// newConnPairMsizeTransport mirrors newConnPairTransport but with a
// configurable msize on both the server (WithMaxMsize) and the client
// (Tversion). Used by I/O benchmarks that need to negotiate larger msizes
// (e.g. 1MiB for size=iounit subtests).
func newConnPairMsizeTransport(tb testing.TB, transport string, root Node, msize uint32, opts ...Option) *connPair {
	tb.Helper()

	if transport == "unix" && runtime.GOOS == "windows" {
		tb.Skipf("unix transport not supported on windows")
	}

	opts = append([]Option{WithMaxMsize(msize), WithLogger(discardLogger())}, opts...)
	srv := New(root, opts...)

	var client, server net.Conn
	switch transport {
	case "pipe":
		client, server = net.Pipe()
	case "unix":
		client, server = unixSocketPair(tb)
	default:
		tb.Fatalf("newConnPairMsizeTransport: unknown transport %q", transport)
	}

	ctx, cancel := context.WithTimeout(tb.Context(), 30*time.Second)
	tb.Cleanup(func() {
		cancel()
		_ = client.Close()
		_ = server.Close()
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.ServeConn(ctx, server)
	}()

	sendTversion(tb, client, msize, "9P2000.L")
	rv := readRversion(tb, client)
	if rv.Version != "9P2000.L" {
		tb.Fatalf("version negotiation failed: got %q", rv.Version)
	}

	return &connPair{client: client, done: done, cancel: cancel}
}

// newConnPairMsize preserves the pipe-only signature for any future callers
// that don't need transport parameterization. Currently every in-tree caller
// uses newConnPairMsizeTransport directly, but the wrapper is retained for API
// parity with newConnPair (walk_test.go) so new pipe-only benchmarks can land
// without re-introducing the helper.
//
//nolint:unused // kept for API parity with newConnPair; see godoc above
func newConnPairMsize(tb testing.TB, root Node, msize uint32, opts ...Option) *connPair {
	return newConnPairMsizeTransport(tb, "pipe", root, msize, opts...)
}

// benchWalkOpen walks from fid to name, allocating newFid, then opens newFid.
// Returns the IOUnit from Rlopen. Must be called before the measurement loop.
// Accepts testing.TB so the Phase 14 strace helper test (plan 14-03) can
// reuse the prelude from a *testing.T context.
func benchWalkOpen(tb testing.TB, cp *connPair, fid, newFid proto.Fid, name string) uint32 {
	tb.Helper()

	// Walk.
	walkFrame := mustEncode(tb, proto.Tag(10), &proto.Twalk{
		Fid:    fid,
		NewFid: newFid,
		Names:  []string{name},
	})
	if _, err := cp.client.Write(walkFrame); err != nil {
		tb.Fatalf("walk write: %v", err)
	}
	if err := drainResponse(cp.client); err != nil {
		tb.Fatalf("walk drain: %v", err)
	}

	// Open — need to decode the response to get IOUnit.
	openFrame := mustEncode(tb, proto.Tag(11), &p9l.Tlopen{
		Fid:   newFid,
		Flags: 0,
	})
	if _, err := cp.client.Write(openFrame); err != nil {
		tb.Fatalf("open write: %v", err)
	}
	// Read and decode the Rlopen to extract IOUnit.
	var hdr [4]byte
	if _, err := io.ReadFull(cp.client, hdr[:]); err != nil {
		tb.Fatalf("open read hdr: %v", err)
	}
	size := binary.LittleEndian.Uint32(hdr[:])
	body := make([]byte, size-4)
	if _, err := io.ReadFull(cp.client, body); err != nil {
		tb.Fatalf("open read body: %v", err)
	}
	// body[0] = type, body[1:3] = tag, body[3:] = Rlopen payload
	if proto.MessageType(body[0]) != proto.TypeRlopen {
		tb.Fatalf("expected Rlopen, got type %d", body[0])
	}
	// Rlopen payload: QID[13] + IOUnit[4]
	iounit := binary.LittleEndian.Uint32(body[3+13 : 3+13+4])
	return iounit
}

const (
	benchFileSize = 128 * 1024 * 1024 // 128MiB
	numOffsets    = 1024              // pre-generated random offset count
)

// newBenchTree creates a directory with a single 128MiB file named "data" for
// benchmarking. The file is pre-filled with deterministic random bytes.
// Accepts testing.TB so the Phase 14 strace helper test (plan 14-03) can
// reuse the same fixture from a *testing.T context.
func newBenchTree(tb testing.TB) *benchDir {
	tb.Helper()
	var gen QIDGenerator

	dir := &benchDir{}
	dir.Init(gen.Next(proto.QTDIR), dir)

	data := make([]byte, benchFileSize)
	rng := rand.New(rand.NewPCG(42, 0))
	for i := range data {
		data[i] = byte(rng.IntN(256))
	}

	file := &benchFile{data: data}
	file.Init(gen.Next(proto.QTFILE), file)
	dir.AddChild("data", file.EmbeddedInode())

	return dir
}

// nonPayloaderRread wraps a proto.Rread but deliberately does NOT embed it,
// so *nonPayloaderRread does not satisfy proto.Payloader (EncodeFixed/Payload
// are NOT method-promoted because inner is a field, not an embedded type).
// This forces sendResponseInline to take the EncodeTo fallback branch
// (server/conn.go:833) — the pre-v1.1.18 copy path.
//
// Used by BenchmarkServerRead_{4K,1M}/encode=copy subtests to reconstruct a
// same-binary copy-path baseline per phase 14 D-01 (no git-checkout baselines
// per D-02). The bufPtr field carries ownership of the pooled buffer backing
// the original Rread.Data slice; Release returns it to bufpool after the
// writev completes, preserving the same payload-lifetime contract as
// pooledRread in server/bridge.go.
type nonPayloaderRread struct {
	inner  proto.Rread // NOTE: field, NOT embedded — prevents Payloader method promotion
	bufPtr *[]byte
}

// Type delegates to the inner Rread.
func (r *nonPayloaderRread) Type() proto.MessageType { return r.inner.Type() }

// EncodeTo delegates to the inner Rread — this writes count[4] + data[count]
// into the body buffer, which is exactly the pre-Payloader copy path.
func (r *nonPayloaderRread) EncodeTo(w io.Writer) error { return r.inner.EncodeTo(w) }

// DecodeFrom delegates to the inner Rread (unused on the server side but
// required to satisfy proto.Message).
func (r *nonPayloaderRread) DecodeFrom(rd io.Reader) error { return r.inner.DecodeFrom(rd) }

// Release returns the pooled buffer that backed the original Rread.Data
// slice. Called by sendResponseInline after the writev completes.
func (r *nonPayloaderRread) Release() { bufpool.PutMsgBuf(r.bufPtr) }

// Compile-time guards: MUST be a proto.Message and a releaser, MUST NOT
// be a proto.Payloader. The absence of the Payloader guard line is
// intentional — see nonPayloaderRread godoc and the
// TestNonPayloaderRread_DoesNotSatisfyPayloader runtime assertion.
var _ proto.Message = (*nonPayloaderRread)(nil)
var _ releaser = (*nonPayloaderRread)(nil)

// forceCopyMiddleware intercepts *pooledRread responses and swaps them for
// *nonPayloaderRread with the same bufPtr, preserving the Release contract
// while forcing the EncodeTo fallback path in sendResponseInline. Used by
// BenchmarkServerRead_{4K,1M}/encode=copy subtests to produce a same-binary
// A/B against the production Payloader path (phase 14 D-01, PERF-07.1/.2).
//
// Non-pooledRread responses pass through unchanged so this middleware is
// safe to install on any bench harness that touches other message types
// (Tversion, Tattach, Twalk, Tlopen, Tclunk all produce non-pooledRread
// responses).
var forceCopyMiddleware Middleware = func(next Handler) Handler {
	return func(ctx context.Context, tag proto.Tag, msg proto.Message) proto.Message {
		resp := next(ctx, tag, msg)
		if pr, ok := resp.(*pooledRread); ok {
			return &nonPayloaderRread{inner: pr.Rread, bufPtr: pr.bufPtr}
		}
		return resp
	}
}

// treadOffsetPos is the byte offset of the Offset field in a Tread wire frame.
// Wire layout: size[4] + type[1] + tag[2] + fid[4] = 11 bytes before offset[8].
const treadOffsetPos = 4 + 1 + 2 + 4

// twriteOffsetPos is the same — Twrite has identical header layout before offset.
const twriteOffsetPos = treadOffsetPos

func BenchmarkRead(b *testing.B) {
	cases := []struct {
		name     string
		readSize uint32
		msize    uint32
		random   bool
	}{
		{"size=4k/pattern=random", 4096, 65536, true},
		{"size=4k/pattern=sequential", 4096, 65536, false},
		{"size=iounit/pattern=sequential", 0, 1024 * 1024, false}, // 0 = use iounit
	}

	for _, transport := range []string{"pipe", "unix"} {
		b.Run("transport="+transport, func(b *testing.B) {
			for _, tc := range cases {
				b.Run(tc.name, func(b *testing.B) {
					root := newBenchTree(b)
					cp := newConnPairMsizeTransport(b, transport, root, tc.msize)
					b.Cleanup(func() { cp.close(b) })

					benchAttachFid0(b, cp)
					iounit := benchWalkOpen(b, cp, 0, 1, "data")

					readSize := tc.readSize
					if readSize == 0 {
						readSize = iounit
					}

					// Pre-encode a Tread frame and locate the offset field for patching.
					frame := mustEncode(b, proto.Tag(1), &proto.Tread{
						Fid:    1,
						Offset: 0,
						Count:  readSize,
					})

					// Pre-generate offsets.
					maxOffset := uint64(benchFileSize) - uint64(readSize)
					offsets := make([]uint64, numOffsets)
					if tc.random {
						rng := rand.New(rand.NewPCG(99, 0))
						for i := range offsets {
							offsets[i] = rng.Uint64N(maxOffset+1) &^ 0xFFF // 4K-aligned
						}
					} else {
						for i := range offsets {
							offsets[i] = (uint64(i) * uint64(readSize)) % (maxOffset + 1)
						}
					}

					b.ReportAllocs()
					b.SetBytes(int64(readSize))
					var idx int
					for b.Loop() {
						binary.LittleEndian.PutUint64(frame[treadOffsetPos:], offsets[idx%numOffsets])
						if _, err := cp.client.Write(frame); err != nil {
							b.Fatalf("write: %v", err)
						}
						if err := drainResponse(cp.client); err != nil {
							b.Fatalf("drain: %v", err)
						}
						idx++
					}
				})
			}
		})
	}
}

// BenchmarkReadPipelined measures pipelined request throughput under the
// inline-write model (v1.1.15+). Each worker encodes and writev's its
// response independently under writeMu; there is no cross-request
// coalescing (the pre-v1.1.15 writer-goroutine model batched multiple
// queued responses into a single writev). This benchmark validates that
// pipelining multiple in-flight requests still sustains throughput with
// per-response writev.
//
// The burst size (BurstN) controls how many Tread requests are in flight
// simultaneously. The benchmark reports MB/s for the full batch and
// allocs/op for a single request (divides total by BurstN).
func BenchmarkReadPipelined(b *testing.B) {
	const readSize uint32 = 4096
	for _, transport := range []string{"pipe", "unix"} {
		b.Run("transport="+transport, func(b *testing.B) {
			for _, burstN := range []int{1, 4, 16, 64} {
				b.Run(fmt.Sprintf("burst=%d", burstN), func(b *testing.B) {
					root := newBenchTree(b)
					cp := newConnPairMsizeTransport(b, transport, root, 65536)
					b.Cleanup(func() { cp.close(b) })

					benchAttachFid0(b, cp)
					benchWalkOpen(b, cp, 0, 1, "data")

					// Pre-encode one Tread frame and patch the offset per request.
					frame := mustEncode(b, proto.Tag(1), &proto.Tread{
						Fid:    1,
						Offset: 0,
						Count:  readSize,
					})

					offsets := make([]uint64, numOffsets)
					maxOffset := uint64(benchFileSize) - uint64(readSize)
					for i := range offsets {
						offsets[i] = (uint64(i) * uint64(readSize)) % (maxOffset + 1)
					}

					b.ReportAllocs()
					b.SetBytes(int64(readSize) * int64(burstN))
					var idx int
					for b.Loop() {
						// Send burstN requests without reading any responses yet.
						for range burstN {
							binary.LittleEndian.PutUint64(frame[treadOffsetPos:], offsets[idx%numOffsets])
							if _, err := cp.client.Write(frame); err != nil {
								b.Fatalf("write: %v", err)
							}
							idx++
						}
						// Drain all burstN responses.
						for range burstN {
							if err := drainResponse(cp.client); err != nil {
								b.Fatalf("drain: %v", err)
							}
						}
					}
				})
			}
		})
	}
}

func BenchmarkWrite(b *testing.B) {
	cases := []struct {
		name      string
		writeSize uint32
		msize     uint32
		random    bool
	}{
		{"size=4k/pattern=random", 4096, 65536, true},
		{"size=4k/pattern=sequential", 4096, 65536, false},
		{"size=iounit/pattern=sequential", 0, 1024 * 1024, false}, // 0 = use iounit
	}

	for _, transport := range []string{"pipe", "unix"} {
		b.Run("transport="+transport, func(b *testing.B) {
			for _, tc := range cases {
				b.Run(tc.name, func(b *testing.B) {
					root := newBenchTree(b)
					cp := newConnPairMsizeTransport(b, transport, root, tc.msize)
					b.Cleanup(func() { cp.close(b) })

					benchAttachFid0(b, cp)
					iounit := benchWalkOpen(b, cp, 0, 1, "data")

					// Max write payload that fits in a Twrite frame:
					// msize - size[4] - type[1] - tag[2] - fid[4] - offset[8] - count[4] = msize - 23.
					maxWriteData := tc.msize - 23
					writeSize := tc.writeSize
					if writeSize == 0 {
						writeSize = maxWriteData
					}
					_ = iounit // iounit is for Rread; Twrite has more overhead

					// Pre-fill write payload with deterministic data.
					payload := make([]byte, writeSize)
					rng := rand.New(rand.NewPCG(77, 0))
					for i := range payload {
						payload[i] = byte(rng.IntN(256))
					}

					// Pre-encode a Twrite frame.
					frame := mustEncode(b, proto.Tag(1), &proto.Twrite{
						Fid:    1,
						Offset: 0,
						Data:   payload,
					})

					// Pre-generate offsets.
					maxOffset := uint64(benchFileSize) - uint64(writeSize)
					offsets := make([]uint64, numOffsets)
					if tc.random {
						rng := rand.New(rand.NewPCG(99, 0))
						for i := range offsets {
							offsets[i] = rng.Uint64N(maxOffset+1) &^ 0xFFF // 4K-aligned
						}
					} else {
						for i := range offsets {
							offsets[i] = (uint64(i) * uint64(writeSize)) % (maxOffset + 1)
						}
					}

					b.ReportAllocs()
					b.SetBytes(int64(writeSize))
					var idx int
					for b.Loop() {
						binary.LittleEndian.PutUint64(frame[twriteOffsetPos:], offsets[idx%numOffsets])
						if _, err := cp.client.Write(frame); err != nil {
							b.Fatalf("write: %v", err)
						}
						if err := drainResponse(cp.client); err != nil {
							b.Fatalf("drain: %v", err)
						}
						idx++
					}
				})
			}
		})
	}
}

// BenchmarkServerRead4K_UnderGC reproduces the Q-side seq_read_4k variance
// workload over unix domain socket: 4 KiB sequential reads with runtime.GC()
// fired every 1000 iterations INSIDE the hot loop (D-03). The handoff's
// Target G acceptance (PERF-04.3) is stddev/mean <= 10% over 10 independent
// runs, measured via `benchstat` on the MB/s column. This bench is the
// producer of that measurement — the 10-run aggregation lives in the
// phase summary, not here.
//
// Transport is hard-coded to "unix" because net.Pipe hides writev-related
// variance: the Payloader path (proto.Payloader, v1.1.18) emits a single
// writev(header + payload) on unix, sequential Write(header); Write(payload)
// on pipe. The variance this bench measures is the bufpool drain-feedback
// interaction with writev batching — absent writev, there's no signal.
// See CLAUDE.md §Performance and 13-RESEARCH.md Anti-Pattern 2.
//
// Wrapped in a transport=unix subtest so future comparison with pipe can
// be added by expanding the slice without restructuring the bench.
func BenchmarkServerRead4K_UnderGC(b *testing.B) {
	const readSize uint32 = 4096
	const msize uint32 = 1 << 20 // 1 MiB negotiated (matches BenchmarkRead/size=iounit)

	for _, transport := range []string{"unix"} {
		b.Run("transport="+transport, func(b *testing.B) {
			root := newBenchTree(b)
			cp := newConnPairMsizeTransport(b, transport, root, msize)
			b.Cleanup(func() { cp.close(b) })

			benchAttachFid0(b, cp)
			benchWalkOpen(b, cp, 0, 1, "data")

			// Pre-encode the Tread frame ONCE. Patch offset per-iter via treadOffsetPos.
			frame := mustEncode(b, proto.Tag(1), &proto.Tread{
				Fid:    1,
				Offset: 0,
				Count:  readSize,
			})

			// Pre-generate sequential offsets covering 128 MiB in 4K steps.
			offsets := make([]uint64, numOffsets)
			maxOffset := uint64(benchFileSize) - uint64(readSize)
			for i := range offsets {
				offsets[i] = (uint64(i) * uint64(readSize)) % (maxOffset + 1)
			}

			b.ReportAllocs()
			b.SetBytes(int64(readSize))
			var idx int
			for b.Loop() {
				binary.LittleEndian.PutUint64(frame[treadOffsetPos:], offsets[idx%numOffsets])
				if _, err := cp.client.Write(frame); err != nil {
					b.Fatalf("write: %v", err)
				}
				if err := drainResponse(cp.client); err != nil {
					b.Fatalf("drain: %v", err)
				}
				idx++
				if idx%1000 == 0 {
					runtime.GC()
				}
			}
		})
	}
}

// BenchmarkServerRead_{4K,1M} is the phase-14 A/B harness for PERF-07.1 and
// PERF-07.2. The encode axis selects between the production Payloader path
// (encode=payloader: sendResponseInline detects proto.Payloader on the
// response and emits hdr + fixedBody[count] + payload as 3 iovecs via
// net.Buffers.WriteTo) and a test-only copy-path baseline (encode=copy:
// forceCopyMiddleware swaps *pooledRread for *nonPayloaderRread so
// sendResponseInline takes the EncodeTo fallback branch, serialising count
// + data into the body buffer and writev'ing hdr + body as 2 iovecs with
// a memcpy of the payload through the pooled body slice).
//
// Acceptance (to be confirmed by human at the plan 14-01 checkpoint via
// benchstat -col encode):
//   - transport=unix/encode=payloader MB/s >= 1.10 * transport=unix/encode=copy (PERF-07.1, 4K)
//   - transport=unix/encode=payloader MB/s >= 1.05 * transport=unix/encode=copy (PERF-07.2, 1M)
//
// The transport=pipe subtests exist for correctness A/B (plan 14-02) and
// are NOT the PERF acceptance signal: net.Pipe does not implement the
// buffersWriter type-assertion path in net.Buffers.WriteTo, so writev is
// never emitted on pipe regardless of payload shape. See phase 14 RESEARCH
// Pitfall 5.

// BenchmarkServerRead_4K measures server-side Rread throughput at 4 KiB
// reads over the default 64 KiB msize (D-07). Subtests: transport={unix,
// pipe} x encode={payloader,copy}. The encode=copy arm installs
// forceCopyMiddleware via WithMiddleware to force the pre-Payloader copy
// path (D-01).
func BenchmarkServerRead_4K(b *testing.B) {
	const readSize uint32 = 4096
	const msize uint32 = 65536 // D-07: default msize — 4K reads fit trivially

	for _, transport := range []string{"unix", "pipe"} {
		b.Run("transport="+transport, func(b *testing.B) {
			for _, encode := range []string{"payloader", "copy"} {
				b.Run("encode="+encode, func(b *testing.B) {
					root := newBenchTree(b)

					var opts []Option
					if encode == "copy" {
						opts = append(opts, WithMiddleware(forceCopyMiddleware))
					}
					cp := newConnPairMsizeTransport(b, transport, root, msize, opts...)
					b.Cleanup(func() { cp.close(b) })

					benchAttachFid0(b, cp)
					_ = benchWalkOpen(b, cp, 0, 1, "data")

					// Pre-encode a Tread frame once and patch the offset per-iter via
					// treadOffsetPos. Keeps the hot loop free of message-encoding allocs.
					frame := mustEncode(b, proto.Tag(1), &proto.Tread{
						Fid:    1,
						Offset: 0,
						Count:  readSize,
					})

					// Pre-generate sequential-ish 4K-aligned offsets covering 128 MiB.
					maxOffset := uint64(benchFileSize) - uint64(readSize)
					offsets := make([]uint64, numOffsets)
					for i := range offsets {
						offsets[i] = (uint64(i) * uint64(readSize)) % (maxOffset + 1)
					}

					b.ReportAllocs()
					b.SetBytes(int64(readSize))
					var idx int
					for b.Loop() {
						binary.LittleEndian.PutUint64(frame[treadOffsetPos:], offsets[idx%numOffsets])
						if _, err := cp.client.Write(frame); err != nil {
							b.Fatalf("write: %v", err)
						}
						if err := drainResponse(cp.client); err != nil {
							b.Fatalf("drain: %v", err)
						}
						idx++
					}
				})
			}
		})
	}
}

// BenchmarkServerRead_1M measures server-side Rread throughput at 1 MiB
// reads over a negotiated 1 MiB msize (D-06). Subtests: transport={unix,
// pipe} x encode={payloader,copy}. Same shape as BenchmarkServerRead_4K
// but exercises the large-payload path where the Payloader writev lift
// should be smaller in percentage terms (PERF-07.2 threshold >= 5%).
func BenchmarkServerRead_1M(b *testing.B) {
	const readSize uint32 = 1 << 20 // D-06: 1 MiB
	const msize uint32 = 1 << 20    // D-06: 1 MiB negotiated msize to fit the read

	for _, transport := range []string{"unix", "pipe"} {
		b.Run("transport="+transport, func(b *testing.B) {
			for _, encode := range []string{"payloader", "copy"} {
				b.Run("encode="+encode, func(b *testing.B) {
					root := newBenchTree(b)

					var opts []Option
					if encode == "copy" {
						opts = append(opts, WithMiddleware(forceCopyMiddleware))
					}
					cp := newConnPairMsizeTransport(b, transport, root, msize, opts...)
					b.Cleanup(func() { cp.close(b) })

					benchAttachFid0(b, cp)
					_ = benchWalkOpen(b, cp, 0, 1, "data")

					frame := mustEncode(b, proto.Tag(1), &proto.Tread{
						Fid:    1,
						Offset: 0,
						Count:  readSize,
					})

					maxOffset := uint64(benchFileSize) - uint64(readSize)
					offsets := make([]uint64, numOffsets)
					for i := range offsets {
						offsets[i] = (uint64(i) * uint64(readSize)) % (maxOffset + 1)
					}

					b.ReportAllocs()
					b.SetBytes(int64(readSize))
					var idx int
					for b.Loop() {
						binary.LittleEndian.PutUint64(frame[treadOffsetPos:], offsets[idx%numOffsets])
						if _, err := cp.client.Write(frame); err != nil {
							b.Fatalf("write: %v", err)
						}
						if err := drainResponse(cp.client); err != nil {
							b.Fatalf("drain: %v", err)
						}
						idx++
					}
				})
			}
		})
	}
}

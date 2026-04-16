package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dotwaffle/ninep/internal/bufpool"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"

	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// Benchmarks in this file establish baselines for Phase 8 (buffer pooling) and
// Phase 9 (resource limits). Every subtest uses key=value naming so benchstat
// can group and diff across runs, and every b.Loop / pb.Next body is paired
// with b.ReportAllocs so the allocs/op column is populated.
//
// Fid/wire semantics assumed throughout:
//   - newConnPair already negotiates Tversion; callers send post-Tversion ops.
//   - Tattach wires fid 0 to the server's root before measurement loops.
//   - Round-trip benches expect a response per request (possibly Rlerror when
//     the target node does not implement the capability). An Rlerror is still
//     a complete dispatch round-trip and exercises the same code paths a
//     successful response does.

// mustEncode serialises a message to its 9P2000.L wire frame and is used for
// declaring frame []byte once per benchmark, outside the measurement loop.
func mustEncode(tb testing.TB, tag proto.Tag, msg proto.Message) []byte {
	tb.Helper()
	var buf bytes.Buffer
	if err := p9l.Encode(&buf, tag, msg); err != nil {
		tb.Fatalf("mustEncode %s: %v", msg.Type(), err)
	}
	return buf.Bytes()
}

// drainResponse consumes a single size-prefixed 9P frame from c and discards
// the body. Errors surface so benchmarks can b.Fatal on protocol breakage.
func drainResponse(c net.Conn) error {
	var hdr [4]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		return err
	}
	size := binary.LittleEndian.Uint32(hdr[:])
	if size < 4 {
		return fmt.Errorf("drainResponse: short frame size %d", size)
	}
	_, err := io.CopyN(io.Discard, c, int64(size)-4)
	return err
}

// benchAttachFid0 attaches fid 0 to the root over cp.client. Separated from
// newConnPair so the measurement loop starts with a fid that handlers can use.
func benchAttachFid0(b *testing.B, cp *connPair) {
	b.Helper()
	wire := mustEncode(b, proto.Tag(1), &proto.Tattach{
		Fid:   0,
		Afid:  proto.NoFid,
		Uname: "u",
		Aname: "",
	})
	if _, err := cp.client.Write(wire); err != nil {
		b.Fatalf("attach write: %v", err)
	}
	if err := drainResponse(cp.client); err != nil {
		b.Fatalf("attach drain: %v", err)
	}
}

// BenchmarkRoundTrip measures the full client->server->client flow for a
// handful of representative operations. Parameterized over transport={pipe,unix}
// so a single bench run reports both the synthetic-baseline (net.Pipe — no
// socket buffering, no writev) and the production-realistic (unix domain
// socket — supports writev, real socket-buffer semantics) numbers. Use cases
// cover a small fixed-size request (getattr) and a larger data request (read).
func BenchmarkRoundTrip(b *testing.B) {
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
	for _, transport := range []string{"pipe", "unix"} {
		b.Run("transport="+transport, func(b *testing.B) {
			for _, tc := range cases {
				b.Run(tc.name, func(b *testing.B) {
					root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})
					cp := newConnPairTransport(b, transport, root)
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
		})
	}
}

// BenchmarkRoundTripWithOTel mirrors BenchmarkRoundTrip with the OTel noop
// tracer and meter providers wired in. The delta against BenchmarkRoundTrip
// quantifies middleware overhead when no spans or samples are recorded.
// Parameterized over transport={pipe,unix} on the same axis as BenchmarkRoundTrip.
func BenchmarkRoundTripWithOTel(b *testing.B) {
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
	for _, transport := range []string{"pipe", "unix"} {
		b.Run("transport="+transport, func(b *testing.B) {
			for _, tc := range cases {
				b.Run(tc.name, func(b *testing.B) {
					root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})
					cp := newConnPairTransport(b, transport, root,
						WithTracer(tracenoop.NewTracerProvider()),
						WithMeter(metricnoop.NewMeterProvider()),
					)
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
		})
	}
}

// BenchmarkReadDecode isolates the recv-path allocation pattern from
// server/conn.go (handleRequest's read+decode under recvMu). Post-08-04 it
// mirrors the bufpool.GetMsgBuf / PutMsgBuf flow so the benchmark reflects
// production behaviour and benchstat shows the allocation win delivered by
// PERF-02.
//
// Intentionally NOT parameterized over transport=unix. The benchmark relies
// on net.Pipe's synchronous Write↔Read semantics: each producer Write blocks
// until the consumer's matching Read advances, which keeps the recv-path
// allocation pattern deterministic across iterations. A unix-domain socket
// has a kernel send buffer that lets the producer race ahead of the
// consumer, changing what the benchmark measures (socket throughput rather
// than recv-path alloc isolation). Use BenchmarkRoundTrip/transport=unix or
// BenchmarkRead/transport=unix for unix-side numbers.
//
// The producer goroutine has no explicit stop channel. A net.Pipe Write blocks
// until a matching Read occurs, so a select-based stop signal would not be
// observed mid-Write. Cleanup closes the pipe; the in-flight Write returns
// io.ErrClosedPipe, the loop observes the error, and the goroutine exits.
func BenchmarkReadDecode(b *testing.B) {
	frame := mustEncode(b, proto.Tag(1), &p9l.Tgetattr{
		Fid:         0,
		RequestMask: proto.AttrAll,
	})

	client, serverConn := net.Pipe()
	go func() {
		for {
			if _, err := client.Write(frame); err != nil {
				return
			}
		}
	}()
	b.Cleanup(func() {
		_ = client.Close()
		_ = serverConn.Close()
	})

	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	for b.Loop() {
		// Mirror the post-08-04 recv path: pooled msg buf, ReadFull into the
		// pooled slice, reconstruct a complete frame for p9l.Decode, then
		// release the pooled slice (safe because DecodeFrom copies).
		size, err := proto.ReadUint32(serverConn)
		if err != nil {
			b.Fatalf("read size: %v", err)
		}
		bufPtr := bufpool.GetMsgBuf(int(size - 4))
		buf := (*bufPtr)[:size-4]
		if _, err := io.ReadFull(serverConn, buf); err != nil {
			bufpool.PutMsgBuf(bufPtr)
			b.Fatalf("read body: %v", err)
		}
		// Reconstruct a complete frame so p9l.Decode can parse the header.
		full := make([]byte, 4+len(buf))
		binary.LittleEndian.PutUint32(full[:4], size)
		copy(full[4:], buf)
		bufpool.PutMsgBuf(bufPtr)
		if _, _, err := p9l.Decode(bytes.NewReader(full)); err != nil {
			b.Fatalf("decode: %v", err)
		}
	}
}

// BenchmarkFidTableContention stresses the fidTable RWMutex under b.RunParallel
// with 90% reads (ft.get) and 10% writes (ft.setPath) against a pre-populated
// table sized at 100/1000/10000 entries. This is the synthetic counterpart to
// BenchmarkWalkClunk's realistic load.
//
// Per-worker RNG seeding draws from workerSeq (atomic.Uint64) BEFORE the
// for pb.Next() loop. Seeding inside the loop via pb.Next() is wrong on two
// counts: pb.Next() returns bool (not an integer), and it consumes iteration
// budget that the benchmark framework uses for per-op accounting.
func BenchmarkFidTableContention(b *testing.B) {
	sizes := []int{100, 1000, 10000}
	for _, n := range sizes {
		b.Run("fids="+strconv.Itoa(n), func(b *testing.B) {
			ft := newFidTable()
			for i := range n {
				if err := ft.add(proto.Fid(i), &fidState{}, 0); err != nil {
					b.Fatalf("pre-populate fid %d: %v", i, err)
				}
			}
			var workerSeq atomic.Uint64
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				seed := workerSeq.Add(1)
				rng := rand.New(rand.NewPCG(seed, 0x9E3779B97F4A7C15))
				for pb.Next() {
					fid := proto.Fid(rng.IntN(n))
					_ = ft.get(fid) // 90% reads.
					if rng.IntN(10) == 0 {
						ft.setPath(fid, "/bench/path")
					}
				}
			})
		})
	}
}

// BenchmarkWalkCycle exercises the walk+clunk hot path for SEC-01/SEC-02
// zero-cost verification. The two subtests differ only in the WithMaxFids
// option: benchstat against limit=none to confirm that WithMaxFids, when
// never fired, imposes no measurable overhead (branch predictor ensures the
// extra compare is free when the cap is never hit).
//
// Pattern: Twalk clone (Names=[]) allocates a new fid; Tclunk frees it.
// This exercises fidTable.add + fidTable.clunk once per iteration -- the
// code paths modified by Plan 09-02.
//
// benchstat-friendly: both subtests use key=value naming so the file can be
// split and diffed; see .planning/phases/09/09-03-ZEROCOST.md for the
// methodology and verdict.
func BenchmarkWalkCycle(b *testing.B) {
	cases := []struct {
		name string
		opts []Option
	}{
		{name: "limit=none", opts: nil},
		{name: "limit=huge", opts: []Option{WithMaxFids(100_000)}},
	}
	for _, transport := range []string{"pipe", "unix"} {
		b.Run("transport="+transport, func(b *testing.B) {
			for _, tc := range cases {
				b.Run(tc.name, func(b *testing.B) {
					root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})
					cp := newConnPairTransport(b, transport, root, tc.opts...)
					b.Cleanup(func() { cp.close(b) })

					benchAttachFid0(b, cp)

					walkFrame := mustEncode(b, proto.Tag(2), &proto.Twalk{
						Fid:    0,
						NewFid: 1,
						Names:  nil,
					})
					clunkFrame := mustEncode(b, proto.Tag(3), &proto.Tclunk{Fid: 1})

					b.ReportAllocs()
					b.SetBytes(int64(len(walkFrame) + len(clunkFrame)))
					for b.Loop() {
						if _, err := cp.client.Write(walkFrame); err != nil {
							b.Fatalf("walk write: %v", err)
						}
						if err := drainResponse(cp.client); err != nil {
							b.Fatalf("walk drain: %v", err)
						}
						if _, err := cp.client.Write(clunkFrame); err != nil {
							b.Fatalf("clunk write: %v", err)
						}
						if err := drainResponse(cp.client); err != nil {
							b.Fatalf("clunk drain: %v", err)
						}
					}
				})
			}
		})
	}
}

// BenchmarkWalkClunk exercises the realistic attach->walk->clunk path under
// parallel load. Each worker owns a disjoint fid range to avoid "fid in use"
// collisions. A client-side mutex serialises writes to the shared net.Pipe
// because net.Pipe is a synchronous Reader+Writer pair: concurrent client
// Writes from multiple goroutines would interleave on the wire and corrupt
// framing. The server's read loop dispatches each request to a per-request
// goroutine that contends on fidTable, so real contention still shows up in
// this benchmark -- the mutex only serialises client-side I/O.
func BenchmarkWalkClunk(b *testing.B) {
	for _, transport := range []string{"pipe", "unix"} {
		b.Run("transport="+transport, func(b *testing.B) {
			root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})
			cp := newConnPairTransport(b, transport, root)
			b.Cleanup(func() { cp.close(b) })

			benchAttachFid0(b, cp)

			// Allocate disjoint fid ranges per worker to prevent collisions. Fid 0 is
			// reserved for the attached root.
			var nextBase atomic.Uint32
			nextBase.Store(1)

			var mu sync.Mutex
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				base := nextBase.Add(1_000_000) - 1_000_000 + 1
				var local uint32
				for pb.Next() {
					newFid := proto.Fid(base + local)
					local++
					walkWire := mustEncode(b, proto.Tag(1), &proto.Twalk{
						Fid:    0,
						NewFid: newFid,
						Names:  nil,
					})
					clunkWire := mustEncode(b, proto.Tag(1), &proto.Tclunk{Fid: newFid})

					mu.Lock()
					_, werr := cp.client.Write(walkWire)
					if werr == nil {
						werr = drainResponse(cp.client)
					}
					if werr == nil {
						_, werr = cp.client.Write(clunkWire)
					}
					if werr == nil {
						werr = drainResponse(cp.client)
					}
					mu.Unlock()
					if werr != nil {
						b.Fatalf("walk+clunk: %v", werr)
					}
				}
			})
		})
	}
}

// makeBenchDirents builds n synthetic directory entries suitable for
// EncodeDirents benchmarks. Names are short-but-variable ("file0", "file1", ...)
// so per-entry size is representative of a real listing.
func makeBenchDirents(n int) []proto.Dirent {
	dirents := make([]proto.Dirent, n)
	for i := range dirents {
		dirents[i] = proto.Dirent{
			QID:    proto.QID{Type: proto.QTFILE, Version: 0, Path: uint64(i)},
			Offset: uint64(i + 1),
			Type:   proto.DT_REG,
			Name:   "file" + strconv.Itoa(i),
		}
	}
	return dirents
}

// BenchmarkEncodeDirents measures allocations and time for EncodeDirents at
// n=10/100/1000 entries. Phase 8 PERF-03 will target the bytes.Buffer
// allocation inside EncodeDirents for pooling.
func BenchmarkEncodeDirents(b *testing.B) {
	sizes := []int{10, 100, 1000}
	for _, n := range sizes {
		b.Run("n="+strconv.Itoa(n), func(b *testing.B) {
			dirents := makeBenchDirents(n)
			const maxBytes uint32 = 65536
			b.ReportAllocs()
			for b.Loop() {
				_, _ = EncodeDirents(dirents, maxBytes)
			}
		})
	}
}

// benchCreateDir is a bench-only directory node that implements NodeCreater
// (server/node.go:85-88) so BenchmarkCreateWriteClose exercises Tlcreate
// through the full bridge path rather than bouncing off Rlerror(ENOSYS).
// Created files live in-memory, share no storage with the parent, and are
// never read back — the bench measures create→write→clunk round-trip cost,
// not file content persistence.
//
// Compare with benchDir in io_bench_test.go (lines 58-63) which only
// implements Open; benchCreateDir is a strict superset.
type benchCreateDir struct {
	Inode
	gen *QIDGenerator // for creating child QIDs
}

func (d *benchCreateDir) Open(_ context.Context, _ uint32) (FileHandle, uint32, error) {
	return nil, 0, nil
}

func (d *benchCreateDir) Create(_ context.Context, name string, flags uint32, mode proto.FileMode, gid uint32) (Node, FileHandle, uint32, error) {
	_ = name
	_ = flags
	_ = mode
	_ = gid
	child := &benchFile{data: make([]byte, 4096)}
	child.Init(d.gen.Next(proto.QTFILE), child)
	// Do not d.AddChild — the bench does not re-walk; the created child
	// is referenced only via the Tlcreate-returned fid for the same iteration.
	return child, nil, 0, nil
}

// newBenchCreateTree builds a benchCreateDir root for BenchmarkCreateWriteClose.
func newBenchCreateTree(b *testing.B) *benchCreateDir {
	b.Helper()
	var gen QIDGenerator
	dir := &benchCreateDir{gen: &gen}
	dir.Init(gen.Next(proto.QTDIR), dir)
	return dir
}

// BenchmarkCreateWriteClose measures the 4-message create-write-close pipeline
// (Twalk(clone) → Tlcreate → Twrite(4K) → Tclunk) over a unix domain socket.
// Mirrors the Q workload's small_file_create pattern without a kernel 9p
// mount: every iteration creates a fresh file, writes 4 KiB, and clunks.
//
// Acceptance (PERF-05.1, success criterion 4): allocs/op reported here MUST
// be at least 4 lower than the `-tags nocache` run of the same bench.
// The A/B comparison is produced via `benchstat` on the two runs.
//
// Transport is unix-only: pipe hides writev-related effects, and the Q
// workload runs over kernel 9p (unix socket + v9fs). See 13-RESEARCH.md
// Pitfall 7 and CLAUDE.md §Performance.
func BenchmarkCreateWriteClose(b *testing.B) {
	root := newBenchCreateTree(b)
	cp := newConnPairMsizeTransport(b, "unix", root, 65536)
	b.Cleanup(func() { cp.close(b) })

	benchAttachFid0(b, cp)

	writeData := make([]byte, 4096)
	b.ReportAllocs()
	b.SetBytes(int64(len(writeData)))
	var seq uint32
	for b.Loop() {
		seq++
		newFid := proto.Fid(1000 + seq)
		walk := mustEncode(b, proto.Tag(1), &proto.Twalk{
			Fid:    0,
			NewFid: newFid,
			Names:  nil,
		})
		create := mustEncode(b, proto.Tag(2), &p9l.Tlcreate{
			Fid:   newFid,
			Name:  "f" + strconv.FormatUint(uint64(seq), 10),
			Flags: 0x0002 | 0x0040, // O_RDWR | O_CREAT
			Mode:  0o644,
		})
		write := mustEncode(b, proto.Tag(3), &proto.Twrite{
			Fid:    newFid,
			Offset: 0,
			Data:   writeData,
		})
		clunk := mustEncode(b, proto.Tag(4), &proto.Tclunk{Fid: newFid})

		for _, frame := range [][]byte{walk, create, write, clunk} {
			if _, err := cp.client.Write(frame); err != nil {
				b.Fatalf("write: %v", err)
			}
			if err := drainResponse(cp.client); err != nil {
				b.Fatalf("drain: %v", err)
			}
		}
	}
}

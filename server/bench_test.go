package server

import (
	"bytes"
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

// BenchmarkRoundTrip measures the full client->server->client flow over a
// net.Pipe connection for a handful of representative operations. Use cases
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
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})
			cp := newConnPair(b, root)
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

// BenchmarkRoundTripWithOTel mirrors BenchmarkRoundTrip with the OTel noop
// tracer and meter providers wired in. The delta against BenchmarkRoundTrip
// quantifies middleware overhead when no spans or samples are recorded.
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
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})
			cp := newConnPair(b, root,
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
}

// BenchmarkReadDecode isolates the readLoop allocation pattern from
// server/conn.go. Post-08-04 it mirrors the new bufpool.GetMsgBuf /
// PutMsgBuf flow so the benchmark reflects production behaviour and
// benchstat shows the allocation win delivered by PERF-02.
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
		// Mirror the post-08-04 readLoop: pooled msg buf, ReadFull into the
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
				if err := ft.add(proto.Fid(i), &fidState{}); err != nil {
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

// BenchmarkWalkClunk exercises the realistic attach->walk->clunk path under
// parallel load. Each worker owns a disjoint fid range to avoid "fid in use"
// collisions. A client-side mutex serialises writes to the shared net.Pipe
// because net.Pipe is a synchronous Reader+Writer pair: concurrent client
// Writes from multiple goroutines would interleave on the wire and corrupt
// framing. The server's read loop dispatches each request to a per-request
// goroutine that contends on fidTable, so real contention still shows up in
// this benchmark -- the mutex only serialises client-side I/O.
func BenchmarkWalkClunk(b *testing.B) {
	root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})
	cp := newConnPair(b, root)
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
			Type:   uint8(proto.QTFILE),
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

package server

import (
	"context"
	"encoding/binary"
	"io"
	"math/rand/v2"
	"net"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// I/O benchmarks measure read/write throughput and allocation pressure through
// the full server stack (encode → readLoop → dispatch → bridge → writeLoop →
// encode). Each subtest uses key=value naming for benchstat grouping, and all
// call b.ReportAllocs + b.SetBytes for allocs/op and MB/s columns.

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

// newConnPairMsize creates a connPair with a configurable msize for both the
// server and the client Tversion negotiation. The standard newConnPair
// hardcodes 65536 for both sides.
func newConnPairMsize(tb testing.TB, root Node, msize uint32, opts ...Option) *connPair {
	tb.Helper()

	opts = append([]Option{WithMaxMsize(msize), WithLogger(discardLogger())}, opts...)
	srv := New(root, opts...)

	client, server := net.Pipe()

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

// benchWalkOpen walks from fid to name, allocating newFid, then opens newFid.
// Returns the IOUnit from Rlopen. Must be called before the measurement loop.
func benchWalkOpen(b *testing.B, cp *connPair, fid, newFid proto.Fid, name string) uint32 {
	b.Helper()

	// Walk.
	walkFrame := mustEncode(b, proto.Tag(10), &proto.Twalk{
		Fid:    fid,
		NewFid: newFid,
		Names:  []string{name},
	})
	if _, err := cp.client.Write(walkFrame); err != nil {
		b.Fatalf("walk write: %v", err)
	}
	if err := drainResponse(cp.client); err != nil {
		b.Fatalf("walk drain: %v", err)
	}

	// Open — need to decode the response to get IOUnit.
	openFrame := mustEncode(b, proto.Tag(11), &p9l.Tlopen{
		Fid:   newFid,
		Flags: 0,
	})
	if _, err := cp.client.Write(openFrame); err != nil {
		b.Fatalf("open write: %v", err)
	}
	// Read and decode the Rlopen to extract IOUnit.
	var hdr [4]byte
	if _, err := io.ReadFull(cp.client, hdr[:]); err != nil {
		b.Fatalf("open read hdr: %v", err)
	}
	size := binary.LittleEndian.Uint32(hdr[:])
	body := make([]byte, size-4)
	if _, err := io.ReadFull(cp.client, body); err != nil {
		b.Fatalf("open read body: %v", err)
	}
	// body[0] = type, body[1:3] = tag, body[3:] = Rlopen payload
	if proto.MessageType(body[0]) != proto.TypeRlopen {
		b.Fatalf("expected Rlopen, got type %d", body[0])
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
func newBenchTree(b *testing.B) *benchDir {
	b.Helper()
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

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			root := newBenchTree(b)
			cp := newConnPairMsize(b, root, tc.msize)
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

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			root := newBenchTree(b)
			cp := newConnPairMsize(b, root, tc.msize)
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
}

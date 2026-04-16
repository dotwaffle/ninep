package server

import (
	"sync"
	"testing"

	"github.com/dotwaffle/ninep/proto"
)

// treadPool backs the "approach=pooled" variants. Declared in the test
// file because production code does not pool message structs (pooling
// caused a ~15% regression at the server level due to cross-P sync.Pool
// overhead — see BenchmarkRead commentary).
var treadPool = sync.Pool{
	New: func() any { return &proto.Tread{} },
}

// msgalloc_bench_test.go isolates the per-request message-struct allocation
// cost to see whether pooling Tread (and friends) would materially reduce
// GC pressure. The allocation in production is ~24 bytes per request (size
// of a *proto.Tread), multiplied by request rate; at 300K req/sec that's
// ~7.2 MB/sec of short-lived allocation.
//
// Three approaches are measured:
//   - current:  &proto.Tread{} + interface conversion (escapes to heap)
//   - pooled:   sync.Pool-backed &proto.Tread + interface conversion
//   - value:    stack-allocated proto.Tread, no interface (contract change)
//
// All three do the same work: decode a Tread body from a pre-encoded byte
// slice and assert one of its fields. This keeps the comparison apples-to-
// apples — only the allocation strategy differs.

// a minimal 11-byte Tread body: fid[4] + offset[8] + count[4] = 16 actually
// but including msg decode path specifics... just make a valid body.
var treadBody = func() []byte {
	// fid=0, offset=0, count=4096 → 4 + 8 + 4 = 16 bytes
	b := make([]byte, 16)
	// offsets already 0
	// count=4096 at bytes 12..16
	b[12] = 0x00
	b[13] = 0x10 // 4096 little-endian
	return b
}()

func BenchmarkMessageAlloc(b *testing.B) {
	b.Run("approach=current", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			// Mirror conn.newMessage: allocate fresh struct, return as
			// proto.Message interface. This is the production pattern.
			var msg proto.Message = &proto.Tread{}
			// Force use so the compiler cannot eliminate the allocation.
			if msg.Type() != proto.TypeTread {
				b.Fatal("type mismatch")
			}
		}
	})

	b.Run("approach=pooled", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			tr := treadPool.Get().(*proto.Tread)
			*tr = proto.Tread{} // reset
			var msg proto.Message = tr
			if msg.Type() != proto.TypeTread {
				b.Fatal("type mismatch")
			}
			treadPool.Put(tr)
		}
	})

	b.Run("approach=value", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			// Stack-allocated value — no interface conversion.
			// This is what the middleware-contract change would enable:
			// the dispatch path would pass concrete types rather than
			// wrapping in proto.Message.
			var tr proto.Tread
			if tr.Type() != proto.TypeTread {
				b.Fatal("type mismatch")
			}
		}
	})
}

// BenchmarkMessageAllocFullDecode measures the same three approaches but
// including a DecodeFrom call, so the cost of actually using the message
// (not just allocating it) is reflected. The decode path is identical
// across approaches; only the message lifecycle differs.
func BenchmarkMessageAllocFullDecode(b *testing.B) {
	b.Run("approach=current", func(b *testing.B) {
		var br readerFromBytes
		b.ReportAllocs()
		for b.Loop() {
			br.reset(treadBody)
			var msg proto.Message = &proto.Tread{}
			if err := msg.DecodeFrom(&br); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("approach=pooled", func(b *testing.B) {
		var br readerFromBytes
		b.ReportAllocs()
		for b.Loop() {
			br.reset(treadBody)
			tr := treadPool.Get().(*proto.Tread)
			*tr = proto.Tread{}
			var msg proto.Message = tr
			if err := msg.DecodeFrom(&br); err != nil {
				b.Fatal(err)
			}
			treadPool.Put(tr)
		}
	})

	b.Run("approach=value", func(b *testing.B) {
		var br readerFromBytes
		b.ReportAllocs()
		for b.Loop() {
			br.reset(treadBody)
			var tr proto.Tread
			// DecodeFrom has a pointer receiver; taking &tr of a stack
			// variable keeps tr on the stack if escape analysis allows.
			if err := tr.DecodeFrom(&br); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// readerFromBytes is a zero-alloc io.Reader over a []byte slice. The
// bytes.Reader equivalent would suffice but this keeps all three
// approaches free of any reader-allocation noise.
type readerFromBytes struct {
	b   []byte
	off int
}

func (r *readerFromBytes) reset(b []byte) { r.b = b; r.off = 0 }

func (r *readerFromBytes) Read(p []byte) (int, error) {
	if r.off >= len(r.b) {
		return 0, nil // EOF signalled by zero-read for our use
	}
	n := copy(p, r.b[r.off:])
	r.off += n
	return n, nil
}

// Package bufpool provides pooled []byte and *bytes.Buffer reuse for the
// 9P message encode, decode, and recv-path hot paths. It lives under
// internal/ at the module root so only github.com/dotwaffle/ninep/... may
// import it -- Go's internal/ rule gives "internal only" while still
// letting cross-package consumers (proto/p9l, proto/p9u, server) share a
// single pool.
//
// # Three independent pools
//
// The package exposes three pools, sized for distinct workloads:
//
//   - [GetBuf] / [PutBuf] -- *bytes.Buffer for arbitrary-size encode
//     targets. Used by version negotiation and other variable-size
//     encoders that grow opportunistically.
//   - [GetMsgBuf] / [PutMsgBuf] -- bucketed *[]byte for read/readdir
//     bridge buffers and the decode-side message body. Buckets are sized
//     1 KiB / 4 KiB / 64 KiB / 1 MiB to span the dynamic range of 9P
//     traffic without mixing classes.
//   - [GetStringBuf] / [PutStringBuf] -- a separate bucketed pool for
//     proto.ReadString. 9P strings carry a uint16 length prefix, so most
//     strings (names, paths, version, uname) fit comfortably in the
//     128B - 4KiB buckets.
//
// # Why *[]byte, not []byte
//
// sync.Pool boxes its argument into an any interface. A slice header is
// larger than a single machine word, so storing []byte directly forces
// the boxing path to allocate a heap slot for the header. Storing *[]byte
// keeps the slice header on the stack and the pool entries pointer-sized.
// This pattern is documented at the field level on msgBufBuckets.
//
// # Why size-class bucketing
//
// A single pool sized to the worst-case message under workloads that mix
// 1 KiB control messages and 1 MiB reads develops a drain feedback loop
// visible via GODEBUG=gctrace=1: large buffers churn through GC and the
// pool fills with newly allocated 1 MiB slabs every other cycle. Per-class
// bucketing keeps each pool's entries stable across GC cycles and avoids
// promoting small-message allocations into the large-buffer footprint.
// See msgBucketSizes for the chosen size classes and their rationale.
//
// # Pooling boundaries
//
// Encode buffers start at 1 KiB and are retained only while they remain in
// that class. Message buffers use buckets through 1 MiB, matching the default
// msize and the Linux kernel's silent cap. Larger legal messages use fresh
// allocations that are released after the operation.
//
// The cap is a pooling boundary, not a hard limit on message size. A
// server configured with a larger WithMaxMsize still works up to the protocol
// allocation ceiling, but GetMsgBuf serves any buffer above the top bucket from a
// fresh allocation that PutMsgBuf drops to the GC rather than pooling.
// That trades buffer reuse for GC pressure on the oversized path. The
// Linux kernel client never negotiates above 1 MiB, so it never takes
// that path; only an explicitly raised msize does.
//
// # Bucket alignment caveat
//
// All bucket sizes are powers of two and GetMsgBuf(n) returns a buffer
// whose cap is exactly the bucket size, never an arbitrary cap >= n.
// Callers MUST slice to the requested length and MUST NOT resize the
// buffer (e.g. with append beyond cap), because PutMsgBuf rejects
// buffers whose cap does not exactly match a bucket size -- they get
// dropped to GC instead of returning to a bucket they would mis-fit.
package bufpool

import (
	"bytes"
	"sync"
)

// PoolMaxBufSize is the upper bound on pooled buffer capacity. Buffers
// that grow above this cap are released to the GC on PutBuf rather than
// retained in the pool (pool-pollution guard).
//
// 1MiB matches the ninep server default maxMsize and the Linux kernel's
// silent msize cap. Messages larger than this are legal in 9P but the
// kernel will not negotiate above 1MiB; dropping oversized buffers keeps
// pool memory proportional to steady-state traffic, not worst-case.
const PoolMaxBufSize = 1024 * 1024

const initialEncodeBufSize = 1 << 10

var bufPool = sync.Pool{
	New: func() any {
		return bytes.NewBuffer(make([]byte, 0, initialEncodeBufSize))
	},
}

// GetBuf returns a zero-length *bytes.Buffer from the pool.
// Callers MUST call PutBuf(b) when finished (typically via defer).
func GetBuf() *bytes.Buffer {
	b := bufPool.Get().(*bytes.Buffer)
	b.Reset()
	return b
}

// PutBuf returns b to the pool only while it remains in the small encode
// class. Buffers that grew while encoding are dropped so concurrent control
// responses cannot inherit large retained capacities from earlier traffic.
func PutBuf(b *bytes.Buffer) {
	if b.Cap() > initialEncodeBufSize {
		return
	}
	bufPool.Put(b)
}

// msgBucketSizes are the capacity size classes for pooled message buffers.
// Chosen to cover typical 9P message sizes without wasting memory on the
// common case:
//   - 1 KiB:  control messages (Tclunk=7B, Twalk=30B, Tgetattr=15B, etc.)
//     -- ~99% of non-data messages fit here
//   - 4 KiB:  small data reads (matches kernel page size, common FUSE unit)
//   - 64 KiB: medium data reads / readdir fragments
//   - 1 MiB:  msize-scale reads (matches PoolMaxBufSize and kernel cap)
var msgBucketSizes = [...]int{
	1 << 10, // 1 KiB
	1 << 12, // 4 KiB
	1 << 16, // 64 KiB
	1 << 20, // 1 MiB (== PoolMaxBufSize)
}

// msgBufBuckets holds one sync.Pool per size class. Each pool returns
// a *[]byte whose cap is exactly msgBucketSizes[i].
var msgBufBuckets = [len(msgBucketSizes)]sync.Pool{
	{New: func() any { b := make([]byte, 1<<10); return &b }},
	{New: func() any { b := make([]byte, 1<<12); return &b }},
	{New: func() any { b := make([]byte, 1<<16); return &b }},
	{New: func() any { b := make([]byte, 1<<20); return &b }},
}

// msgBucketFor returns the index of the smallest bucket whose capacity is
// >= n, or -1 if n exceeds all buckets.
func msgBucketFor(n int) int {
	for i, size := range msgBucketSizes {
		if n <= size {
			return i
		}
	}
	return -1
}

// GetMsgBuf returns a pointer to a []byte with capacity >= n, drawn from
// the smallest bucket that fits. If n exceeds the largest bucket
// (PoolMaxBufSize, 1 MiB), a fresh buffer of size n is allocated and left
// un-pooled: PutMsgBuf drops it to the GC.
func GetMsgBuf(n int) *[]byte {
	idx := msgBucketFor(n)
	if idx < 0 {
		b := make([]byte, n)
		return &b
	}
	return msgBufBuckets[idx].Get().(*[]byte)
}

// PutMsgBuf returns b to its source bucket iff cap(*b) exactly matches a
// bucket size.
func PutMsgBuf(b *[]byte) {
	c := cap(*b)
	switch c {
	case 1 << 10: // 1 KiB
		*b = (*b)[:c]
		msgBufBuckets[0].Put(b)
	case 1 << 12: // 4 KiB
		*b = (*b)[:c]
		msgBufBuckets[1].Put(b)
	case 1 << 16: // 64 KiB
		*b = (*b)[:c]
		msgBufBuckets[2].Put(b)
	case 1 << 20: // 1 MiB
		*b = (*b)[:c]
		msgBufBuckets[3].Put(b)
	}
}

var stringBucketSizes = [...]int{
	128,
	512,
	1024,
	4096,
}

var stringBufBuckets = [len(stringBucketSizes)]sync.Pool{
	{New: func() any { b := make([]byte, 0, 128); return &b }},
	{New: func() any { b := make([]byte, 0, 512); return &b }},
	{New: func() any { b := make([]byte, 0, 1024); return &b }},
	{New: func() any { b := make([]byte, 0, 4096); return &b }},
}

func stringBucketFor(n int) int {
	for i, size := range stringBucketSizes {
		if n <= size {
			return i
		}
	}
	return -1
}

// GetStringBuf returns a pointer to a []byte suitable for use as a scratch
// buffer for up to n bytes. If n exceeds 4 KiB, a fresh buffer is allocated.
func GetStringBuf(n int) *[]byte {
	idx := stringBucketFor(n)
	if idx < 0 {
		b := make([]byte, 0, n)
		return &b
	}
	b := stringBufBuckets[idx].Get().(*[]byte)
	if cap(*b) < n {
		// Bucket invariant violated: a buffer whose cap is below the
		// bucket's size class was somehow returned to the pool. Return
		// it to its real bucket (if it matches another size class) so
		// the pool is not silently drained, then allocate fresh.
		PutStringBuf(b)
		nb := make([]byte, 0, n)
		return &nb
	}
	*b = (*b)[:0]
	return b
}

// PutStringBuf returns b to its source bucket iff cap(*b) matches a size class.
func PutStringBuf(b *[]byte) {
	c := cap(*b)
	switch c {
	case 128:
		*b = (*b)[:0]
		stringBufBuckets[0].Put(b)
	case 512:
		*b = (*b)[:0]
		stringBufBuckets[1].Put(b)
	case 1024:
		*b = (*b)[:0]
		stringBufBuckets[2].Put(b)
	case 4096:
		*b = (*b)[:0]
		stringBufBuckets[3].Put(b)
	}
}

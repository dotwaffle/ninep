// Package bufpool provides pooled []byte and *bytes.Buffer reuse for the
// 9P message encode, decode, and recv-path hot paths. It lives under
// internal/ at the module root so only github.com/dotwaffle/ninep/... may
// import it -- Go's internal/ rule gives "internal only" while still
// letting cross-package consumers (proto/p9l, proto/p9u, server) share a
// single pool.
//
// # Two-tier design
//
// The package exposes three independent pools, sized for distinct
// workloads:
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
// # Why a 1 MiB cap
//
// PoolMaxBufSize matches the WithMaxMsize default (1 MiB) and the Linux
// kernel's silent 9P msize cap. Buffers above this are released to the GC
// on Put rather than retained, so pool memory stays proportional to
// steady-state traffic instead of growing to the largest message ever
// seen. Messages above 1 MiB are legal in the protocol but the kernel
// will not negotiate above this size, so retaining oversized buffers
// would cost memory for traffic the server can never see again.
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
	"sync/atomic"
)

// Metrics holds counters for pool activity.
type Metrics struct {
	// MsgBufMisses is the count of GetMsgBuf calls that exceeded PoolMaxBufSize
	// and required a fresh allocation.
	MsgBufMisses uint64
	// StringBufMisses is the count of GetStringBuf calls that exceeded the
	// largest bucket (4 KiB) and required a fresh allocation.
	StringBufMisses uint64
}

var (
	msgBufMisses    uint64
	stringBufMisses uint64
)

// ReadMetrics returns a snapshot of the current pool metrics.
func ReadMetrics() Metrics {
	return Metrics{
		MsgBufMisses:    atomic.LoadUint64(&msgBufMisses),
		StringBufMisses: atomic.LoadUint64(&stringBufMisses),
	}
}

// PoolMaxBufSize is the upper bound on pooled buffer capacity. Buffers
// that grow above this cap are released to the GC on PutBuf rather than
// retained in the pool (pool-pollution guard).
//
// 1MiB matches the ninep server default maxMsize and the Linux kernel's
// silent msize cap. Messages larger than this are legal in 9P but the
// kernel will not negotiate above 1MiB; dropping oversized buffers keeps
// pool memory proportional to steady-state traffic, not worst-case.
const PoolMaxBufSize = 1024 * 1024

var bufPool = sync.Pool{
	New: func() any {
		// Pre-grow to PoolMaxBufSize so first-use does not trigger the
		// grow-and-copy path inside bytes.Buffer.
		return bytes.NewBuffer(make([]byte, 0, PoolMaxBufSize))
	},
}

// GetBuf returns a zero-length *bytes.Buffer from the pool.
// Callers MUST call PutBuf(b) when finished (typically via defer).
func GetBuf() *bytes.Buffer {
	b := bufPool.Get().(*bytes.Buffer)
	b.Reset()
	return b
}

// PutBuf returns b to the pool iff its capacity is within PoolMaxBufSize.
// Oversized buffers are dropped and will be GC'd, preventing the pool
// from retaining memory proportional to the largest-ever message.
func PutBuf(b *bytes.Buffer) {
	if b.Cap() > PoolMaxBufSize {
		return
	}
	bufPool.Put(b)
}

// msgBucketSizes are the capacity size classes for pooled message buffers.
// Chosen to cover typical 9P message sizes without wasting memory on the
// common case:
//   - 1 KiB:  control messages (Tclunk=7B, Twalk=30B, Tgetattr=15B, etc.)
//     — ~99% of non-data messages fit here
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
// the smallest bucket that fits. If n exceeds PoolMaxBufSize, a fresh
// buffer of size n is allocated (not pooled).
func GetMsgBuf(n int) *[]byte {
	idx := msgBucketFor(n)
	if idx < 0 {
		atomic.AddUint64(&msgBufMisses, 1)
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
		atomic.AddUint64(&stringBufMisses, 1)
		b := make([]byte, 0, n)
		return &b
	}
	b := stringBufBuckets[idx].Get().(*[]byte)
	if cap(*b) < n {
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

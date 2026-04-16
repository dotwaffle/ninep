// Package bufpool provides a process-wide *bytes.Buffer pool for the
// encode, decode, and readLoop hot paths. It lives under internal/
// at the module root so only github.com/dotwaffle/ninep/... may import
// it -- Go's internal/ rule gives us the "internal only" property that
// CONTEXT.md requires while still letting cross-package consumers
// (proto/p9l, proto/p9u, server) share a single pool.
//
// See .planning/phases/08/08-RESEARCH.md Architecture Patterns §1 for
// the pool-shape rationale and Pitfall 2 for the cap-guard rationale.
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
//             — ~99% of non-data messages fit here
//   - 4 KiB:  small data reads (matches kernel page size, common FUSE unit)
//   - 64 KiB: medium data reads / readdir fragments
//   - 1 MiB:  msize-scale reads (matches PoolMaxBufSize and kernel cap)
//
// Without bucketing, a 7-byte Tclunk would claim a 1 MiB buffer from the
// pool. Under GC pressure (sync.Pool drains every other cycle), the cost
// of refilling 1 MiB buffers was the dominant source of seq_read_4k
// throughput variance observed by the Q consumer — see the Q debug doc
// "ninep-smallfile-seq4k-analysis.md" Target G for the measurement.
var msgBucketSizes = [...]int{
	1 << 10, // 1 KiB
	1 << 12, // 4 KiB
	1 << 16, // 64 KiB
	1 << 20, // 1 MiB (== PoolMaxBufSize)
}

// msgBufBuckets holds one sync.Pool per size class. Each pool returns
// a *[]byte whose cap is exactly msgBucketSizes[i].
//
// The pool stores *[]byte rather than []byte because sync.Pool boxes its
// argument into an `any` interface; a slice header is larger than a word
// and causes the boxing to allocate. Pooling a pointer avoids the box
// alloc (see RESEARCH Pitfall: "Pool pointer not value").
//
// Each New closure hard-codes its size to keep the pools usable as a
// composite literal (sync.Pool has an internal noCopy that forbids
// returning pools by value from a factory function).
var msgBufBuckets = [len(msgBucketSizes)]sync.Pool{
	{New: func() any { b := make([]byte, 1<<10); return &b }},
	{New: func() any { b := make([]byte, 1<<12); return &b }},
	{New: func() any { b := make([]byte, 1<<16); return &b }},
	{New: func() any { b := make([]byte, 1<<20); return &b }},
}

// msgBucketFor returns the index of the smallest bucket whose capacity is
// >= n, or -1 if n exceeds all buckets. Linear search over 4 entries;
// the cost is negligible vs the alternative (pointer indirection + map
// lookup) and the compiler tends to unroll it.
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
// buffer of size n is allocated (not pooled) so pool memory stays
// proportional to steady-state traffic.
// Callers MUST call PutMsgBuf(b) when finished (typically via defer).
func GetMsgBuf(n int) *[]byte {
	idx := msgBucketFor(n)
	if idx < 0 {
		b := make([]byte, n)
		return &b
	}
	return msgBufBuckets[idx].Get().(*[]byte)
}

// PutMsgBuf returns b to its source bucket iff cap(*b) exactly matches a
// bucket size. Buffers with caps outside the bucket set (e.g. oversized
// fresh allocations from the GetMsgBuf > PoolMaxBufSize path, or buffers
// resized by callers) are dropped to GC rather than polluting a bucket
// with a mis-sized entry.
func PutMsgBuf(b *[]byte) {
	c := cap(*b)
	// Bucket sizes are monotonically increasing; a buffer only re-pools if
	// its cap exactly equals one of them.
	for i, size := range msgBucketSizes {
		if c == size {
			// Reset length to full capacity so the next caller sees the
			// full slice.
			*b = (*b)[:c]
			msgBufBuckets[i].Put(b)
			return
		}
	}
	// cap does not match any bucket; drop.
}

// stringBufPool pools raw []byte scratch buffers for proto.ReadString.
// Strings in 9P have a uint16 length prefix, so typical sizes are small
// (names, paths, version strings, uname). Initial cap is 1024 bytes,
// well below PoolMaxBufSize; a separate pool keeps the size class tight.
var stringBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 1024)
		return &b
	},
}

// GetStringBuf returns a pointer to a []byte suitable for use as a scratch
// buffer for up to n bytes. The returned buffer is guaranteed to have
// capacity >= n. If n exceeds PoolMaxBufSize, a fresh buffer is allocated
// (not pooled). If a pooled buffer has insufficient capacity for n, it is
// dropped and a fresh buffer of the required size is allocated; the fresh
// buffer enters the pool on PutStringBuf (assuming it fits under the cap
// guard), gradually growing the pool's effective size class.
// Callers MUST call PutStringBuf(b) when finished.
// The returned slice has length 0; callers reslice as needed.
func GetStringBuf(n int) *[]byte {
	if n > PoolMaxBufSize {
		b := make([]byte, 0, n)
		return &b
	}
	b := stringBufPool.Get().(*[]byte)
	if cap(*b) < n {
		// Pooled buffer too small. Drop it (let GC reclaim) and allocate
		// one sized to n; caller will Put it back and subsequent callers
		// will benefit from the larger size class.
		nb := make([]byte, 0, n)
		return &nb
	}
	return b
}

// PutStringBuf returns b to the pool iff cap(*b) <= PoolMaxBufSize.
func PutStringBuf(b *[]byte) {
	if cap(*b) > PoolMaxBufSize {
		return
	}
	*b = (*b)[:0]
	stringBufPool.Put(b)
}

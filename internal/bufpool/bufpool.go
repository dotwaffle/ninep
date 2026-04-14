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
// 128KB matches the Linux kernel v9fs default msize. Messages larger
// than this are legal in 9P but atypical; dropping oversized buffers
// keeps pool memory proportional to steady-state traffic, not worst-case.
const PoolMaxBufSize = 128 * 1024

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

package server

import (
	"context"
	"runtime"
	"testing"
)

// BenchmarkRequestContext measures the pooled requestCtx get+put cycle.
//
// Subtests:
//   - /hit  — steady-state measurement; the pool is pre-warmed so every
//     iteration measures the sync.Pool fast path. PERF-08.1 binds this
//     subtest to 0 allocs/op.
//   - /miss — forced pool miss via runtime.GC() before each get. sync.Pool
//     drains its per-P caches on GC; the subsequent Get either re-hits
//     whatever the New function returned from a drained pool or allocates.
//     The requestCtx-attributable cost is 1 alloc/op (the struct); any
//     additional allocs reported by the bench are sync.Pool.pinSlow
//     infrastructure overhead triggered by the per-iteration GC. The hard
//     gate for the miss path is TestRequestCtxAllocs (see comment below).
//
// Both subtests use b.Loop() (Go 1.24+ idiom, already used elsewhere in this
// package) and b.ReportAllocs() so the allocs/op column is populated.
//
// The /miss body wraps runtime.GC() in b.StopTimer/b.StartTimer so GC cost
// does not pollute the ns/op column — only the get+put cycle is measured.
//
// See also: TestRequestCtxAllocs in requestctx_test.go, which asserts the
// alloc budget as a hard test gate via testing.AllocsPerRun. This benchmark
// exists for benchstat A/B comparisons and live inspection.
func BenchmarkRequestContext(b *testing.B) {
	parent := context.Background()

	b.Run("hit", func(b *testing.B) {
		// Warm every per-P pool slot so the measurement loop observes
		// steady-state behaviour (no allocations on Get).
		for range 32 {
			putRequestCtx(getRequestCtx(parent))
		}
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			rctx := getRequestCtx(parent)
			_ = rctx.Err() // simulate a handler touching ctx
			putRequestCtx(rctx)
		}
	})

	b.Run("miss", func(b *testing.B) {
		// Deterministically draining sync.Pool is tricky: per-P caches
		// are invisible to callers and are only cleared on GC. A full GC
		// before every iteration drains both the primary and victim
		// caches, so the next Get has to fall through to New (or, if
		// another goroutine has already Put a value back since the GC,
		// to the freshly-repopulated pool).
		//
		// Note on the reported allocs/op count: per-iteration runtime.GC
		// also drains sync.Pool's per-P local array, so the next Pool.Get
		// hits sync.Pool.pinSlow which allocates the per-P array anew.
		// That is infrastructure overhead, not requestCtx overhead — it
		// adds ~1 alloc/op on top of the fresh *requestCtx from New().
		// The load-bearing alloc gate for the miss path is
		// TestRequestCtxAllocs (AllocsPerRun after two forced GCs),
		// which isolates the requestCtx allocation from pinSlow noise.
		// This benchmark exists for benchstat comparisons and live
		// inspection; treat its allocs/op column as informational.
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			b.StopTimer()
			runtime.GC()
			b.StartTimer()
			rctx := getRequestCtx(parent)
			_ = rctx.Err()
			putRequestCtx(rctx)
		}
	})
}

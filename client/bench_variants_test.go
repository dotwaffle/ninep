// Package client_test contains variant-matrix benchmarks for the client
// library. Per 24-CONTEXT.md D-02 / D-04, these live in a SEPARATE file
// from the SC-4 mirror benches in client/bench_test.go so benchstat's
// axis-matching against v1.2.0 HEAD stays clean (the mirror benches use
// only `transport=` while the variant matrix uses `encoding=` / `pool_size=`;
// folding them together would mismatch benchstat's column extraction).
//
// The variant matrix is reference-only (D-13): output populates a
// reference table in 24-VERIFICATION.md. These benches do NOT gate any
// release.
//
// Matrix: {encoding=p9l|p9u} × {pool_size=1|3|8|16} = 8 subtests.
//
// Benchstat column extraction:
//
//	-col encoding     → p9l vs p9u at same pool_size
//	-col pool_size    → saturation curve at same dialect
//
// Research Pitfall 8 / pool.Cache cap: allocs/op rising at pool_size > 3
// is the bounded-cache signal, not a regression. internal/pool.Cache is
// capped at 3 slots per type. The pool_size axis maps to maxInflight per
// research §Pattern 3 (NOT a literal pool-cap API change — the cache cap
// is a constant by design).
//
// Encoding-axis caveat (24-04 SUMMARY): the server has no public option
// to force .u advertisement and the client's Dial hardcodes a "9P2000.L"
// proposal (client/dial.go), so the encoding subtest name is currently
// structural only — both p9l and p9u subtests run the .L codec end-to-end.
// The axis is preserved so future work that adds dialect forcing can fill
// in real .u numbers without renaming subtests (which would break any
// historical benchstat comparison).
//
// Discretion (24-CONTEXT.md "Claude's Discretion"): variant uses
// transport=unix only — pipe doubles CI time and the writev signal lives
// only on unix. The mirror benches in bench_test.go cover the pipe axis.
package client_test

import (
	"os"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/client/clienttest"
	"github.com/dotwaffle/ninep/server"
)

// newBenchClientWithInflight pairs a server+client over the unix
// transport with the supplied maxInflight cap and (notionally) the
// supplied dialect. Used only by the variant-matrix benches in this
// file; the SC-4 mirror benches in bench_test.go use [newBenchClient].
//
// dialect: "p9l" → default; "p9u" → currently a no-op (no server-side
// option exists to force .u advertisement, and Dial proposes "9P2000.L"
// — see file-level godoc for the caveat). Reserved for future use.
func newBenchClientWithInflight(tb testing.TB, _ string, root server.Node, msize uint32, maxInflight int) *client.Conn {
	tb.Helper()
	_, cli := clienttest.UnixPair(tb, root,
		clienttest.WithMsize(msize),
		clienttest.WithClientOpts(client.WithMaxInflight(maxInflight)),
	)
	return cli
}

// BenchmarkClientReadVariants_4K sweeps the {encoding × pool_size} matrix
// to document allocs/op behaviour as a function of dialect choice and
// concurrency-pressure on the bounded R-message cache.
//
// Interpretation:
//   - encoding=p9l vs p9u: both dialects encode/decode Rread identically
//     on the hot path; any sec/op delta is noise-floor territory. Today
//     both subtests exercise the .L codec (see file-level godoc).
//   - pool_size = maxInflight:
//     1 — serial; cache never saturates; minimum allocs.
//     3 — matches internal/pool.Cache cap; at steady state the cache
//     fully services the working set.
//     8 — 2.5× cache cap; cache saturates on bursts; allocs rise.
//     16 — 5× cache cap; sustained pressure; allocs rise further.
//
// Rising allocs at pool_size > 3 is EXPECTED (24-RESEARCH.md Pitfall 8),
// not a regression. The signal informs future cache-cap tuning work.
//
// Per D-13, this bench is reference only; SC-4 uses only
// [BenchmarkClientRead_4K] (mirror-exact, single-axis transport).
//
// Each worker calls cli.OpenFile to obtain a distinct *opened* fid so
// parallel ReadAts do not serialise on f.mu (independent fids share the
// underlying server node but each has its own offset, mutex, and
// per-file lock). The fids share the Conn-level tag allocator — this is
// the correct measurement model for pool_size saturation.
//
// File.Clone is intentionally NOT used: Clone produces an unopened fid
// (Twalk(oldFid, newFid, nil)), and the subsequent ReadAt would return
// EBADF until that fid is also opened. Using cli.OpenFile per worker is
// dialect-agnostic (it dispatches to Lopen on .L and Open on .u inside
// session.go) and gives the worker a ready-to-read handle.
func BenchmarkClientReadVariants_4K(b *testing.B) {
	const readSize uint32 = 4096
	const msize uint32 = 65536

	for _, dialect := range []string{"p9l", "p9u"} {
		b.Run("encoding="+dialect, func(b *testing.B) {
			for _, maxInflight := range []int{1, 3, 8, 16} {
				b.Run("pool_size="+strconv.Itoa(maxInflight), func(b *testing.B) {
					root := newBenchTree(b)
					cli := newBenchClientWithInflight(b, dialect, root, msize, maxInflight)
					// Attach once; each worker then opens its own fid.
					if _, err := cli.Attach(b.Context(), "bench", ""); err != nil {
						b.Fatalf("attach: %v", err)
					}
					b.Cleanup(func() {
						if r := cli.Root(); r != nil {
							_ = r.Close()
						}
					})
					offsets := preGeneratedOffsets(readSize)

					b.ReportAllocs()
					b.SetBytes(int64(readSize))

					var seq atomic.Uint64
					b.RunParallel(func(pb *testing.PB) {
						workerFile, err := cli.OpenFile(b.Context(), "data", os.O_RDONLY, 0)
						if err != nil {
							b.Fatalf("OpenFile: %v", err)
						}
						defer func() { _ = workerFile.Close() }()

						dst := make([]byte, readSize)
						for pb.Next() {
							idx := seq.Add(1) - 1
							off := offsets[int(idx)%numOffsets]
							if _, err := workerFile.ReadAt(dst, int64(off)); err != nil {
								b.Fatalf("ReadAt: %v", err)
							}
						}
					})
				})
			}
		})
	}
}

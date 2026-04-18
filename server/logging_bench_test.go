package server

import (
	"encoding/binary"
	"io"
	"log/slog"
	"testing"

	"github.com/dotwaffle/ninep/proto"
)

// Logging middleware overhead benchmarks.
//
// These benchmarks isolate the per-request cost of NewLoggingMiddleware
// when the configured slog logger is filtering out the Debug records the
// middleware emits. Two configurations are compared:
//
//   - cfg=no_middleware  -- server with no logging middleware attached
//     (the WithLogger discardLogger() default in
//     the test harness is unrelated to the
//     middleware chain).
//   - cfg=debug_disabled -- server with NewLoggingMiddleware attached
//     via WithMiddleware, fed a logger whose
//     handler is set to slog.LevelInfo and writes
//     to io.Discard so any record that escaped
//     the level filter is also a no-op syscall.
//
// The middleware always constructs its three slog.Attr values
// (op/duration/error) before calling logger.LogAttrs, regardless of
// whether the handler will keep the record. Audit Fix #8 proposes
// gating the LogAttrs call behind logger.Enabled(ctx, slog.LevelDebug)
// to skip that construction when the level is filtered. This benchmark
// quantifies the headroom available for that guard.
//
// Workload mirrors BenchmarkRead's 4 KiB sequential read, identical to
// BenchmarkOTelMiddleware in otel_bench_test.go, so the two
// middleware-overhead benches are directly comparable.
//
// Per CLAUDE.md: bench output goes to /tmp/claude/, not /tmp.
func BenchmarkLoggingMiddleware(b *testing.B) {
	const readSize uint32 = 4096

	// Logger whose handler filters at Info -- the middleware emits at
	// Debug, so every record is discarded by the handler's level check.
	infoLogger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cases := []struct {
		name string
		opts []Option
	}{
		{name: "cfg=no_middleware", opts: nil},
		{
			name: "cfg=debug_disabled",
			opts: []Option{WithMiddleware(NewLoggingMiddleware(infoLogger))},
		},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			root := newBenchTree(b)
			cp := newConnPair(b, root, tc.opts...)
			b.Cleanup(func() { cp.close(b) })

			benchAttachFid0(b, cp)
			benchWalkOpen(b, cp, 0, 1, "data")

			frame := mustEncode(b, proto.Tag(1), &proto.Tread{
				Fid:    1,
				Offset: 0,
				Count:  readSize,
			})

			maxOffset := uint64(benchFileSize) - uint64(readSize)
			offsets := make([]uint64, numOffsets)
			for i := range offsets {
				offsets[i] = (uint64(i) * uint64(readSize)) % (maxOffset + 1)
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

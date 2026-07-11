package server

import (
	"encoding/binary"
	"testing"

	"github.com/dotwaffle/ninep/proto"

	"go.opentelemetry.io/otel"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
)

// OTel middleware overhead benchmarks.
//
// These benchmarks isolate the per-request cost of the metric path inside
// the OTel middleware so the audit's measure-first decision (Fix #5) can be
// made on numbers rather than intuition. Two configurations are compared:
//
//   - cfg=baseline    -- no OTel middleware (no WithMeter / WithTracer)
//   - cfg=noop_meter  -- WithMeter(noop.NewMeterProvider()) only
//
// The tracer path is intentionally omitted: the audit finding targets the
// meter (Float64Histogram.Record / Int64UpDownCounter.Add) hot path, and
// mixing in tracing would obscure the signal. BenchmarkRoundTripWithOTel
// in bench_test.go covers the combined-providers case.
//
// Workload mirrors BenchmarkRead's 4 KiB sequential-read configuration so
// the comparison is apples-to-apples with the existing read baseline. We
// pre-encode a Tread frame outside the loop and patch the Offset field
// each iteration -- identical to the BenchmarkRead pattern -- so allocs
// reported here are server-side only.
func BenchmarkOTelMiddleware(b *testing.B) {
	const readSize uint32 = 4096

	cases := []struct {
		name string
		opts []Option
	}{
		{name: "cfg=baseline", opts: nil},
		{name: "cfg=noop_meter", opts: []Option{WithMeter(metricnoop.NewMeterProvider())}},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			root := newBenchTree(b)
			cp := newConnPair(b, root, tc.opts...)
			b.Cleanup(func() { cp.close(b) })

			benchAttachFid0(b, cp)
			benchWalkOpen(b, cp, 0, 1, "data")

			// Pre-encode a Tread frame; patch the offset each iteration.
			frame := mustEncode(b, proto.Tag(1), &proto.Tread{
				Fid:    1,
				Offset: 0,
				Count:  readSize,
			})

			// Pre-generate sequential, 4K-aligned offsets that walk the
			// whole 128 MiB benchFile.
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

// BenchmarkHandleRead_NoOTel is the control case: server built with NO
// telemetry options. Reports the steady-state allocs/op and ns/op for
// a 4 KiB Tread round-trip without any OTel middleware in the chain.
//
// Pair with BenchmarkHandleRead_NoopOTel under -count=10 -benchmem
// and compare via benchstat. Acceptance: allocs/op MUST be equal;
// ns/op within 1%. If ns/op exceeds 1% but allocs/op equality holds,
// surface at checkpoint - ns/op at 4 KiB lives at timing
// noise-floor.
func BenchmarkHandleRead_NoOTel(b *testing.B) {
	const readSize uint32 = 4096

	root := newBenchTree(b)
	cp := newConnPair(b, root) // no telemetry options -- control
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
}

// BenchmarkHandleRead_NoopOTel is the treatment case: server built
// with WithTracer(otel.GetTracerProvider()) +
// WithMeter(otel.GetMeterProvider()) against an uninitialized OTel
// SDK, matching explicit no-op provider wiring. The middleware remains
// installed, so this benchmark measures the cost of that explicit choice.
//
// Note: this benchmark relies on global OTel state staying noop. No
// other test or benchmark in the server package calls otel.SetTracerProvider
// or otel.SetMeterProvider (verified via grep; setupOTelTest uses
// WithTracer/WithMeter on the server only, not the global). Safe.
func BenchmarkHandleRead_NoopOTel(b *testing.B) {
	const readSize uint32 = 4096

	root := newBenchTree(b)
	cp := newConnPair(b, root,
		WithTracer(otel.GetTracerProvider()),
		WithMeter(otel.GetMeterProvider()),
	)
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
}

package server

import (
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// NewTestTracerProvider returns a TracerProvider with an in-memory exporter.
func NewTestTracerProvider(tb testing.TB) (*sdktrace.TracerProvider, *tracetest.InMemoryExporter) {
	tb.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	tb.Cleanup(func() { _ = tp.Shutdown(tb.Context()) })
	return tp, exp
}

// NewTestMeterProvider returns a MeterProvider with a manual reader.
func NewTestMeterProvider(tb testing.TB) (*sdkmetric.MeterProvider, *sdkmetric.ManualReader) {
	tb.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	tb.Cleanup(func() { _ = mp.Shutdown(tb.Context()) })
	return mp, reader
}

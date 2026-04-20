package otelutil

import (
	"testing"

	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// NewTestTracerProvider returns a TracerProvider with an in-memory exporter.
func NewTestTracerProvider(tb testing.TB) (*trace.TracerProvider, *tracetest.InMemoryExporter) {
	tb.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := trace.NewTracerProvider(trace.WithSyncer(exp))
	tb.Cleanup(func() { _ = tp.Shutdown(tb.Context()) })
	return tp, exp
}

// NewTestMeterProvider returns a MeterProvider with a manual reader.
func NewTestMeterProvider(tb testing.TB) (*metric.MeterProvider, metric.Reader) {
	tb.Helper()
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	tb.Cleanup(func() { _ = mp.Shutdown(tb.Context()) })
	return mp, reader
}

// GetMetric finds a metric by name in the ResourceMetrics.
func GetMetric(rm metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for _, sm := range rm.ScopeMetrics {
		for i := range sm.Metrics {
			if sm.Metrics[i].Name == name {
				return &sm.Metrics[i]
			}
		}
	}
	return nil
}

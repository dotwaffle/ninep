package server

import (
	"bytes"
	"context"
	"time"

	"github.com/dotwaffle/ninep/proto"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// instrumentationName is the OTel instrumentation scope name used for all
// tracers and meters created by this package.
const instrumentationName = "github.com/dotwaffle/ninep/server"

// WithTracer sets the OpenTelemetry TracerProvider for the server. When set,
// an OTel middleware is automatically prepended to the middleware chain,
// producing a span for every 9P operation. If not set, no tracing overhead
// is incurred.
func WithTracer(tp trace.TracerProvider) Option {
	return func(s *Server) { s.tracerProvider = tp }
}

// WithMeter sets the OpenTelemetry MeterProvider for the server. When set,
// an OTel middleware is automatically prepended to the middleware chain,
// recording duration, request/response sizes, and active request counts. If
// not set, no metrics overhead is incurred.
func WithMeter(mp metric.MeterProvider) Option {
	return func(s *Server) { s.meterProvider = mp }
}

// otelInstruments holds all OTel metric instruments for a connection. Created
// once per connection in newOTelMiddleware so instruments are not allocated
// per-request.
type otelInstruments struct {
	duration   metric.Float64Histogram
	reqSize    metric.Int64Counter
	respSize   metric.Int64Counter
	activeReqs metric.Int64UpDownCounter
}

// newOTelMiddleware creates a Middleware that instruments every 9P operation
// with OTel spans and metrics. Instruments are created once in the constructor.
// The conn is used to resolve fid paths and protocol version for span attributes.
func newOTelMiddleware(tp trace.TracerProvider, mp metric.MeterProvider, c *conn) Middleware {
	tracer := tp.Tracer(instrumentationName)
	meter := mp.Meter(instrumentationName)

	inst := otelInstruments{
		duration: must(meter.Float64Histogram("ninep.server.duration",
			metric.WithUnit("s"),
			metric.WithDescription("Duration of 9P server operations"),
		)),
		reqSize: must(meter.Int64Counter("ninep.server.request.size",
			metric.WithUnit("By"),
			metric.WithDescription("Size of 9P request messages"),
		)),
		respSize: must(meter.Int64Counter("ninep.server.response.size",
			metric.WithUnit("By"),
			metric.WithDescription("Size of 9P response messages"),
		)),
		activeReqs: must(meter.Int64UpDownCounter("ninep.server.active_requests",
			metric.WithDescription("Number of active 9P requests"),
		)),
	}

	return func(next Handler) Handler {
		return func(ctx context.Context, tag proto.Tag, msg proto.Message) proto.Message {
			opName := msg.Type().String()

			// Start span with initial RPC attributes.
			ctx, span := tracer.Start(ctx, opName,
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					attribute.String("rpc.system.name", "9p"),
					attribute.String("rpc.method", opName),
				),
			)
			defer span.End()

			// Active request gauge.
			inst.activeReqs.Add(ctx, 1)
			defer inst.activeReqs.Add(ctx, -1)

			// Guard expensive attribute computation behind IsRecording.
			if span.IsRecording() {
				if fid, ok := fidFromMessage(msg); ok {
					span.SetAttributes(attribute.Int64("ninep.fid", int64(fid)))
					if fs := c.fids.get(fid); fs != nil {
						span.SetAttributes(attribute.String("ninep.path", fs.path))
					}
				}
				span.SetAttributes(attribute.String("ninep.protocol", c.protocol.String()))
			}

			// Measure request size.
			var reqBuf bytes.Buffer
			if err := msg.EncodeTo(&reqBuf); err == nil {
				inst.reqSize.Add(ctx, int64(reqBuf.Len()))
			}

			start := time.Now()
			resp := next(ctx, tag, msg)
			elapsed := time.Since(start).Seconds()

			// Record duration with rpc.method attribute.
			inst.duration.Record(ctx, elapsed,
				metric.WithAttributes(attribute.String("rpc.method", opName)),
			)

			// Measure response size.
			if resp != nil {
				var respBuf bytes.Buffer
				if err := resp.EncodeTo(&respBuf); err == nil {
					inst.respSize.Add(ctx, int64(respBuf.Len()))
				}

				// Set span status to Error for error responses.
				if isErrorResponse(resp) {
					span.SetStatus(codes.Error, opName)
				}
			}

			return resp
		}
	}
}

// connOTelInstruments holds connection-level and fid-level gauge instruments.
// These are lifecycle metrics, not per-request.
type connOTelInstruments struct {
	connGauge metric.Int64UpDownCounter
	fidGauge  metric.Int64UpDownCounter
}

// newConnOTelInstruments creates connection-level metric instruments from the
// given MeterProvider. Returns nil if mp is nil.
func newConnOTelInstruments(mp metric.MeterProvider) *connOTelInstruments {
	if mp == nil {
		return nil
	}
	meter := mp.Meter(instrumentationName)
	return &connOTelInstruments{
		connGauge: must(meter.Int64UpDownCounter("ninep.server.connections",
			metric.WithDescription("Number of active 9P connections"),
		)),
		fidGauge: must(meter.Int64UpDownCounter("ninep.server.fid.count",
			metric.WithDescription("Number of active fids"),
		)),
	}
}

// recordConnChange records a connection count change (+1 or -1).
func (o *connOTelInstruments) recordConnChange(delta int64) {
	if o == nil {
		return
	}
	o.connGauge.Add(context.Background(), delta)
}

// recordFidChange records a fid count change (+1 or -1).
func (o *connOTelInstruments) recordFidChange(delta int64) {
	if o == nil {
		return
	}
	o.fidGauge.Add(context.Background(), delta)
}

// serverOTelInstruments holds server-level (pre-connection) OTel instruments.
// Created once in New when a MeterProvider is configured. Used by the
// ServeConn reject path (before newConn runs), where conn-level instruments
// do not exist.
type serverOTelInstruments struct {
	connectionsRejected metric.Int64Counter
}

// newServerOTelInstruments creates server-level metric instruments from the
// given MeterProvider. Returns nil if mp is nil (zero-cost when no
// MeterProvider is configured).
func newServerOTelInstruments(mp metric.MeterProvider) *serverOTelInstruments {
	if mp == nil {
		return nil
	}
	meter := mp.Meter(instrumentationName)
	return &serverOTelInstruments{
		connectionsRejected: must(meter.Int64Counter("ninep.server.connections_rejected",
			metric.WithDescription("Number of connections rejected due to WithMaxConnections limit"),
		)),
	}
}

// recordConnectionRejected increments the rejected-connection counter. Safe
// on nil receiver (no-op when no MeterProvider is configured).
func (o *serverOTelInstruments) recordConnectionRejected() {
	if o == nil {
		return
	}
	o.connectionsRejected.Add(context.Background(), 1)
}

// must is a generic helper that panics on instrument creation error. OTel
// instrument creation only fails on invalid names, which are compile-time
// constants in this package.
func must[T any](v T, err error) T {
	if err != nil {
		panic("otel instrument creation: " + err.Error())
	}
	return v
}

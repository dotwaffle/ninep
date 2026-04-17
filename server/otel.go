package server

import (
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

	// Build a per-message-type cache of metric.MeasurementOption holding the
	// rpc.method attribute. Constructed once at middleware build time so the
	// hot path's duration.Record call avoids the allocation that
	// metric.WithAttributes(attribute.String(...)) would otherwise impose
	// every request. The set of T-message types is closed and known at
	// compile time; iterate the proto.MessageType "Type*" constants used by
	// dispatch and skip the R-prefixed responses (responses never enter
	// middleware as msg).
	opNameAttrs := buildOpNameAttrs()

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

			// Active request gauge -- gated so noop meters skip both the
			// Add(+1) and the cost of registering the deferred Add(-1).
			// The defer must live inside the guard so it only runs when
			// the +1 ran.
			if inst.activeReqs.Enabled(ctx) {
				inst.activeReqs.Add(ctx, 1)
				defer inst.activeReqs.Add(ctx, -1)
			}

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

			// Measure request size. Uses ByteCounter so encoding never
			// allocates or copies bytes; it just sums field widths.
			// Gated by Enabled so noop meters skip even the counting.
			if inst.reqSize.Enabled(ctx) {
				var reqBytes proto.ByteCounter
				if err := msg.EncodeTo(&reqBytes); err == nil {
					inst.reqSize.Add(ctx, int64(reqBytes))
				}
			}

			start := time.Now()
			resp := next(ctx, tag, msg)
			elapsed := time.Since(start).Seconds()

			// Record duration with cached rpc.method attribute. Gated so
			// noop histograms skip the Record call entirely.
			if inst.duration.Enabled(ctx) {
				opt, ok := opNameAttrs[msg.Type()]
				if !ok {
					// Defensive fallback for message types not enumerated
					// in buildOpNameAttrs (should not happen for valid
					// T-messages reaching dispatch).
					opt = metric.WithAttributes(attribute.String("rpc.method", opName))
				}
				inst.duration.Record(ctx, elapsed, opt)
			}

			// Measure response size (same zero-alloc ByteCounter path).
			if resp != nil {
				if inst.respSize.Enabled(ctx) {
					var respBytes proto.ByteCounter
					if err := resp.EncodeTo(&respBytes); err == nil {
						inst.respSize.Add(ctx, int64(respBytes))
					}
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

// probeOTelProviders populates s.tracerRecording and s.meterEnabled via a
// one-time probe of the configured providers. Called exactly once from
// server.New after options apply and after nil providers have been filled
// with noop defaults. Probe objects (span + counter) are discarded
// immediately; only the two booleans are retained.
//
// The probe creates a span with instrumentationName scope and an
// Int64Counter named "probe" in the same scope. If a real OTel SDK is later
// installed via otel.SetTracerProvider / otel.SetMeterProvider AFTER
// server.New returns, the probe instrument may surface as a zero-valued
// "probe" counter under the ninep scope. This is acceptable because Q wires
// OTel BEFORE server.New (per 15-CONTEXT.md canonical refs), so in practice
// the probe never reaches a real SDK. Re-probing to handle post-New provider
// swaps is explicitly deferred (D-04).
//
// Preconditions: s.tracerProvider and s.meterProvider MUST be non-nil. The
// caller (New) ensures this.
func probeOTelProviders(s *Server) {
	// Tracer probe: IsRecording() is false for both noop.NewTracerProvider()
	// and otel.GetTracerProvider() before any SDK is installed.
	tracer := s.tracerProvider.Tracer(instrumentationName)
	_, span := tracer.Start(context.Background(), "probe")
	s.tracerRecording = span.IsRecording()
	span.End()

	// Meter probe: Enabled(ctx) is false for both noop.NewMeterProvider()
	// and otel.GetMeterProvider() before any SDK is installed. Stable API
	// since OTel v1.40.0.
	meter := s.meterProvider.Meter(instrumentationName)
	counter, err := meter.Int64Counter("probe")
	if err != nil {
		// Int64Counter only errors on invalid names; "probe" is valid.
		// Defensive: leave meterEnabled at its zero value (false).
		return
	}
	s.meterEnabled = counter.Enabled(context.Background())
}

// requestMessageTypes lists every T-message type the server may dispatch.
// Used by buildOpNameAttrs to pre-build the metric.MeasurementOption cache.
// Responses (R-prefixed types) and Tlerror (never sent on the wire) are
// excluded -- only request types ever appear as msg in middleware.
var requestMessageTypes = [...]proto.MessageType{
	// Shared base T-messages.
	proto.TypeTversion,
	proto.TypeTauth,
	proto.TypeTattach,
	proto.TypeTflush,
	proto.TypeTwalk,
	proto.TypeTopen,
	proto.TypeTcreate,
	proto.TypeTread,
	proto.TypeTwrite,
	proto.TypeTclunk,
	proto.TypeTremove,
	proto.TypeTstat,
	proto.TypeTwstat,

	// 9P2000.L T-messages.
	proto.TypeTstatfs,
	proto.TypeTlopen,
	proto.TypeTlcreate,
	proto.TypeTsymlink,
	proto.TypeTmknod,
	proto.TypeTrename,
	proto.TypeTreadlink,
	proto.TypeTgetattr,
	proto.TypeTsetattr,
	proto.TypeTxattrwalk,
	proto.TypeTxattrcreate,
	proto.TypeTreaddir,
	proto.TypeTfsync,
	proto.TypeTlock,
	proto.TypeTgetlock,
	proto.TypeTlink,
	proto.TypeTmkdir,
	proto.TypeTrenameat,
	proto.TypeTunlinkat,
}

// buildOpNameAttrs returns a per-T-message-type metric.MeasurementOption map
// holding the rpc.method attribute. Constructing this once at middleware
// build time eliminates the per-request metric.WithAttributes allocation on
// the duration.Record hot path.
func buildOpNameAttrs() map[proto.MessageType]metric.MeasurementOption {
	m := make(map[proto.MessageType]metric.MeasurementOption, len(requestMessageTypes))
	for _, t := range requestMessageTypes {
		m[t] = metric.WithAttributes(attribute.String("rpc.method", t.String()))
	}
	return m
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

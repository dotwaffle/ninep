package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dotwaffle/ninep/internal/protometa"
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
// is incurred. The server never consults the OTel global provider set via
// otel.SetTracerProvider; pass otel.GetTracerProvider() explicitly to route
// spans to the globally installed SDK.
func WithTracer(tp trace.TracerProvider) Option {
	return func(s *Server) {
		s.tracerProvider = tp
		if tp == nil {
			s.setConfigError(errors.New("server: tracer provider must not be nil"))
		}
	}
}

// WithMeter sets the OpenTelemetry MeterProvider for the server. When set,
// an OTel middleware is automatically prepended to the middleware chain,
// recording duration, request/response sizes, and active request counts. If
// not set, no metrics overhead is incurred. The server never consults the
// OTel global provider set via otel.SetMeterProvider; pass
// otel.GetMeterProvider() explicitly to route metrics to the globally
// installed SDK.
func WithMeter(mp metric.MeterProvider) Option {
	return func(s *Server) {
		s.meterProvider = mp
		if mp == nil {
			s.setConfigError(errors.New("server: meter provider must not be nil"))
		}
	}
}

// TracePathFilter decides whether and how a resolved fid path is attached to
// a span. Returning false omits the attribute. Implementations should keep
// output cardinality bounded and avoid exposing tenant or secret names.
type TracePathFilter func(path string) (value string, include bool)

// WithTracePathFilter enables the otherwise-disabled ninep.path span
// attribute. The callback may pass through, redact, hash, or drop each path.
func WithTracePathFilter(filter TracePathFilter) Option {
	return func(s *Server) { s.tracePathFilter = filter }
}

// otelInstruments holds all OTel metric instruments for a connection. Created
// once per Server in newOTelCore so instruments are not allocated
// per-request.
type otelInstruments struct {
	duration   metric.Float64Histogram
	requests   metric.Int64Counter
	reqSize    metric.Int64Counter
	respSize   metric.Int64Counter
	activeReqs metric.Int64UpDownCounter
}

// otelCore holds the per-Server OTel state the request middleware needs:
// the tracer, the request-scoped instruments, and the per-message-type
// attribute cache. Built once in server.New; every connection's middleware
// closure shares it. Instrument creation walks the SDK's instrument
// registry under a mutex, so doing it per connection would both contend
// and re-allocate the ~30-entry attribute map on every accept.
type otelCore struct {
	tracer       trace.Tracer
	inst         otelInstruments
	opNameAttrs  map[proto.MessageType]metric.MeasurementOption
	outcomeAttrs map[proto.MessageType][2]metric.MeasurementOption
}

// newOTelCore builds shared OTel state from explicitly configured providers.
func newOTelCore(tp trace.TracerProvider, mp metric.MeterProvider) (*otelCore, error) {
	meter := mp.Meter(instrumentationName)
	duration, err := meter.Float64Histogram("ninep.server.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of 9P server operations"),
	)
	if err != nil {
		return nil, fmt.Errorf("create duration histogram: %w", err)
	}
	requests, err := meter.Int64Counter("ninep.server.requests",
		metric.WithDescription("Number of completed 9P requests by outcome"),
	)
	if err != nil {
		return nil, fmt.Errorf("create requests counter: %w", err)
	}
	reqSize, err := meter.Int64Counter("ninep.server.request.size",
		metric.WithUnit("By"),
		metric.WithDescription("Size of 9P request messages"),
	)
	if err != nil {
		return nil, fmt.Errorf("create request size counter: %w", err)
	}
	respSize, err := meter.Int64Counter("ninep.server.response.size",
		metric.WithUnit("By"),
		metric.WithDescription("Size of 9P response messages"),
	)
	if err != nil {
		return nil, fmt.Errorf("create response size counter: %w", err)
	}
	activeReqs, err := meter.Int64UpDownCounter("ninep.server.active_requests",
		metric.WithDescription("Number of active 9P requests"),
	)
	if err != nil {
		return nil, fmt.Errorf("create active requests counter: %w", err)
	}
	return &otelCore{
		tracer: tp.Tracer(instrumentationName),
		inst: otelInstruments{
			duration: duration, requests: requests, reqSize: reqSize,
			respSize: respSize, activeReqs: activeReqs,
		},
		// Per-message-type cache of metric.MeasurementOption holding the
		// rpc.method attribute, so the hot path's duration.Record call
		// avoids the allocation metric.WithAttributes would otherwise
		// impose every request. The set of T-message types is closed and
		// known at compile time.
		opNameAttrs:  buildOpNameAttrs(),
		outcomeAttrs: buildOutcomeAttrs(),
	}, nil
}

// middleware returns the per-connection OTel middleware. Only the conn
// binding is per-connection (fid-path and protocol span attributes); all
// instruments and caches come from the shared core.
func (o *otelCore) middleware(c *conn) Middleware {
	tracer := o.tracer
	inst := o.inst
	opNameAttrs := o.opNameAttrs
	outcomeAttrs := o.outcomeAttrs

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
				if fid, ok := protometa.Fid(msg); ok {
					span.SetAttributes(attribute.Int64("ninep.fid", int64(fid)))
					if filter := c.server.tracePathFilter; filter != nil {
						if p := c.fids.getPath(fid); p != "" {
							if value, include := filter(p); include {
								span.SetAttributes(attribute.String("ninep.path", value))
							}
						}
					}
				}
				span.SetAttributes(attribute.String("ninep.protocol", c.protocol.String()))
			}

			// Measure request size. The read loop already knows the wire
			// frame size and records it on the requestCtx, so report it
			// directly rather than walking every field a second time
			// through ByteCounter. The body length the old path summed is
			// the frame size minus the fixed header. Gated by Enabled so
			// noop meters skip even the subtraction.
			if inst.reqSize.Enabled(ctx) {
				if size := requestWireSize(ctx); size >= proto.HeaderSize {
					inst.reqSize.Add(ctx, int64(size)-int64(proto.HeaderSize))
				}
			}

			start := time.Now()
			resp := next(ctx, tag, msg)
			elapsed := time.Since(start).Seconds()
			failed := resp == nil || isErrorResponse(resp)
			if inst.requests.Enabled(ctx) {
				outcomeIndex := 0
				if failed {
					outcomeIndex = 1
				}
				if opts, ok := outcomeAttrs[msg.Type()]; ok {
					inst.requests.Add(ctx, 1, opts[outcomeIndex])
				} else {
					outcome := [2]string{"ok", "error"}[outcomeIndex]
					inst.requests.Add(ctx, 1, metric.WithAttributes(
						attribute.String("rpc.method", opName),
						attribute.String("outcome", outcome),
					))
				}
			}

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
				if failed {
					span.SetStatus(codes.Error, opName)
				}
			}

			return resp
		}
	}
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

func buildOutcomeAttrs() map[proto.MessageType][2]metric.MeasurementOption {
	m := make(map[proto.MessageType][2]metric.MeasurementOption, len(requestMessageTypes))
	for _, messageType := range requestMessageTypes {
		m[messageType] = [2]metric.MeasurementOption{
			metric.WithAttributes(
				attribute.String("rpc.method", messageType.String()),
				attribute.String("outcome", "ok"),
			),
			metric.WithAttributes(
				attribute.String("rpc.method", messageType.String()),
				attribute.String("outcome", "error"),
			),
		}
	}
	return m
}

// Abnormal-event reason attribute values recorded on the
// ninep.server.abnormal_events counter.
const (
	// reasonHandlerPanic: a handler panicked and the server replied EIO.
	reasonHandlerPanic = "handler_panic"
	// reasonFlushWaitTimeout: a Tflush waited out the flush deadline for
	// the flushed handler's response; the connection was closed.
	reasonFlushWaitTimeout = "flush_wait_timeout"
	// reasonDrainTimeout: connection cleanup timed out waiting for
	// inflight handlers to drain.
	reasonDrainTimeout = "drain_timeout"
	// reasonForcedClose: a mid-session Tversion could not drain inflight
	// handlers and the connection was closed.
	reasonForcedClose = "forced_close"
)

// connOTelInstruments holds connection-level and fid-level gauge instruments.
// These are lifecycle metrics, not per-request.
type connOTelInstruments struct {
	connGauge      metric.Int64UpDownCounter
	fidGauge       metric.Int64UpDownCounter
	abnormalEvents metric.Int64Counter
}

// newConnOTelInstruments creates connection-level metric instruments from the
// given MeterProvider. Returns nil if mp is nil.
func newConnOTelInstruments(mp metric.MeterProvider) (*connOTelInstruments, error) {
	if mp == nil {
		return nil, nil
	}
	meter := mp.Meter(instrumentationName)
	connGauge, err := meter.Int64UpDownCounter("ninep.server.connections", metric.WithDescription("Number of active 9P connections"))
	if err != nil {
		return nil, fmt.Errorf("create connections gauge: %w", err)
	}
	fidGauge, err := meter.Int64UpDownCounter("ninep.server.fid.count", metric.WithDescription("Number of active fids"))
	if err != nil {
		return nil, fmt.Errorf("create fid gauge: %w", err)
	}
	abnormalEvents, err := meter.Int64Counter("ninep.server.abnormal_events", metric.WithDescription("Abnormal server events (handler panics, drain timeouts, forced closes), by reason"))
	if err != nil {
		return nil, fmt.Errorf("create abnormal events counter: %w", err)
	}
	return &connOTelInstruments{connGauge: connGauge, fidGauge: fidGauge, abnormalEvents: abnormalEvents}, nil
}

// recordConnChange records a connection count change (+1 or -1).
func (o *connOTelInstruments) recordConnChange(delta int64) {
	if o == nil {
		return
	}
	ctx := context.Background()
	if o.connGauge.Enabled(ctx) {
		o.connGauge.Add(ctx, delta)
	}
}

// recordFidChange records a fid count change (+1 or -1).
func (o *connOTelInstruments) recordFidChange(delta int64) {
	if o == nil {
		return
	}
	ctx := context.Background()
	if o.fidGauge.Enabled(ctx) {
		o.fidGauge.Add(ctx, delta)
	}
}

// recordAbnormalEvent counts one abnormal server event with the given reason
// attribute (one of the reason* constants). These paths are rare by
// definition, so the per-call attribute allocation is acceptable.
func (o *connOTelInstruments) recordAbnormalEvent(reason string) {
	if o == nil {
		return
	}
	ctx := context.Background()
	if o.abnormalEvents.Enabled(ctx) {
		o.abnormalEvents.Add(ctx, 1,
			metric.WithAttributes(attribute.String("reason", reason)))
	}
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
func newServerOTelInstruments(mp metric.MeterProvider) (*serverOTelInstruments, error) {
	if mp == nil {
		return nil, nil
	}
	meter := mp.Meter(instrumentationName)
	connectionsRejected, err := meter.Int64Counter("ninep.server.connections_rejected",
		metric.WithDescription("Number of connections rejected due to WithMaxConnections limit"),
	)
	if err != nil {
		return nil, fmt.Errorf("create connections rejected counter: %w", err)
	}
	return &serverOTelInstruments{connectionsRejected: connectionsRejected}, nil
}

// recordConnectionRejected increments the rejected-connection counter. Safe
// on nil receiver (no-op when no MeterProvider is configured).
func (o *serverOTelInstruments) recordConnectionRejected() {
	if o == nil {
		return
	}
	ctx := context.Background()
	if o.connectionsRejected.Enabled(ctx) {
		o.connectionsRejected.Add(ctx, 1)
	}
}

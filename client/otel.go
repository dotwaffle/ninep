package client

import (
	"context"
	"fmt"
	"time"

	"github.com/dotwaffle/ninep/internal/protometa"
	"github.com/dotwaffle/ninep/proto"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// instrumentationName is the OTel instrumentation scope name used for all
// tracers and meters created by this package.
const instrumentationName = "github.com/dotwaffle/ninep/client"

// otelInstruments holds all OTel metric instruments for a connection.
type otelInstruments struct {
	duration   metric.Float64Histogram
	reqSize    metric.Int64Counter
	respSize   metric.Int64Counter
	activeReqs metric.Int64UpDownCounter
}

func newOTelInstruments(mp metric.MeterProvider) (*otelInstruments, error) {
	meter := mp.Meter(instrumentationName)

	duration, err := meter.Float64Histogram("ninep.client.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of 9P client operations"),
	)
	if err != nil {
		return nil, fmt.Errorf("create duration histogram: %w", err)
	}
	reqSize, err := meter.Int64Counter("ninep.client.request.size",
		metric.WithUnit("By"),
		metric.WithDescription("Size of 9P request messages"),
	)
	if err != nil {
		return nil, fmt.Errorf("create request.size counter: %w", err)
	}
	respSize, err := meter.Int64Counter("ninep.client.response.size",
		metric.WithUnit("By"),
		metric.WithDescription("Size of 9P response messages"),
	)
	if err != nil {
		return nil, fmt.Errorf("create response.size counter: %w", err)
	}
	activeReqs, err := meter.Int64UpDownCounter("ninep.client.active_requests",
		metric.WithDescription("Number of active 9P requests"),
	)
	if err != nil {
		return nil, fmt.Errorf("create active_requests counter: %w", err)
	}

	return &otelInstruments{
		duration:   duration,
		reqSize:    reqSize,
		respSize:   respSize,
		activeReqs: activeReqs,
	}, nil
}

func (c *Conn) startSpan(ctx context.Context, opName string, msg proto.Message) (context.Context, trace.Span) {
	if c.tracer == nil {
		// Return a dedicated no-op span, not whatever span is already in
		// ctx: callers unconditionally defer span.End() and may call
		// span.SetStatus() on error. If ctx carries a live span from the
		// caller's own tracing (this Conn need not be the only
		// instrumented thing on the call path), returning it here would
		// let this operation end or mark-errored a span it doesn't own.
		return ctx, noop.Span{}
	}

	opts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("rpc.system.name", "9p"),
			attribute.String("rpc.method", opName),
			attribute.String("ninep.protocol", c.dialect.String()),
		),
	}

	if fid, ok := protometa.Fid(msg); ok {
		opts = append(opts, trace.WithAttributes(attribute.Int64("ninep.fid", int64(fid))))
	}

	return c.tracer.Start(ctx, opName, opts...)
}

// recordRequestSize records the request body size from the frame size writeT
// already computed, avoiding a re-encode. frameSize is header + body + payload;
// the metric counts the message body, matching the prior EncodeTo-based count.
func (c *Conn) recordRequestSize(ctx context.Context, frameSize uint32) {
	if c.inst == nil || !c.inst.reqSize.Enabled(ctx) {
		return
	}
	c.inst.reqSize.Add(ctx, int64(frameSize)-int64(proto.HeaderSize))
}

func (c *Conn) recordResponse(ctx context.Context, opType proto.MessageType, start time.Time, resp proto.Message) {
	if c.inst == nil {
		return
	}

	if !start.IsZero() && c.inst.duration.Enabled(ctx) {
		opt, ok := c.opNameAttrs[opType]
		if !ok {
			opt = metric.WithAttributes(attribute.String("rpc.method", opType.String()))
		}
		c.inst.duration.Record(ctx, time.Since(start).Seconds(), opt)
	}

	if resp != nil && c.inst.respSize.Enabled(ctx) {
		var respBytes proto.ByteCounter
		if err := resp.EncodeTo(&respBytes); err == nil {
			c.inst.respSize.Add(ctx, int64(respBytes))
		}
	}
}

func (c *Conn) recordZCResponse(ctx context.Context, opType proto.MessageType, start time.Time, n int) {
	if c.inst == nil {
		return
	}

	if !start.IsZero() && c.inst.duration.Enabled(ctx) {
		opt, ok := c.opNameAttrs[opType]
		if !ok {
			opt = metric.WithAttributes(attribute.String("rpc.method", opType.String()))
		}
		c.inst.duration.Record(ctx, time.Since(start).Seconds(), opt)
	}
	if c.inst.respSize.Enabled(ctx) {
		c.inst.respSize.Add(ctx, int64(n))
	}
}

func (c *Conn) recordError(span trace.Span, err error) {
	if c.tracer == nil || err == nil {
		return
	}
	span.SetStatus(codes.Error, err.Error())
	span.RecordError(err)
}

func isErrorResponse(msg proto.Message) bool {
	t := msg.Type()
	return t == proto.TypeRlerror || t == proto.TypeRerror
}

// buildOpNameAttrs returns a per-T-message-type metric.MeasurementOption map
// holding the rpc.method attribute.
func buildOpNameAttrs() map[proto.MessageType]metric.MeasurementOption {
	m := make(map[proto.MessageType]metric.MeasurementOption, len(requestMessageTypes))
	for _, t := range requestMessageTypes {
		m[t] = metric.WithAttributes(attribute.String("rpc.method", t.String()))
	}
	return m
}

// requestMessageTypes lists every T-message type the client may send.
var requestMessageTypes = [...]proto.MessageType{
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

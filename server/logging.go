package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/dotwaffle/ninep/proto"

	"go.opentelemetry.io/otel/trace"
)

// traceHandler wraps a slog.Handler to inject OTel trace context into log
// records. When an active span exists in the context passed to Handle, trace_id
// and span_id are added as structured attributes.
type traceHandler struct {
	inner slog.Handler
}

// NewTraceHandler wraps a slog.Handler with OTel trace ID correlation.
// Log records emitted within an active span context will include trace_id
// and span_id attributes. Use this to wrap custom handlers when providing
// a logger via WithLogger.
func NewTraceHandler(inner slog.Handler) slog.Handler {
	return &traceHandler{inner: inner}
}

// Enabled delegates to the inner handler.
func (h *traceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle adds trace_id and span_id attributes when a valid OTel span context
// is present, then delegates to the inner handler.
func (h *traceHandler) Handle(ctx context.Context, r slog.Record) error {
	sc := trace.SpanContextFromContext(ctx)
	if sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.inner.Handle(ctx, r)
}

// WithAttrs returns a new traceHandler wrapping the inner handler's WithAttrs
// result.
func (h *traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceHandler{inner: h.inner.WithAttrs(attrs)}
}

// WithGroup returns a new traceHandler wrapping the inner handler's WithGroup
// result.
func (h *traceHandler) WithGroup(name string) slog.Handler {
	return &traceHandler{inner: h.inner.WithGroup(name)}
}

// NewLoggingMiddleware returns a Middleware that logs each 9P request at
// Debug level with structured attributes: op (operation type), duration
// (elapsed time), and error (whether the response was an error).
// The logger should be wrapped with NewTraceHandler for trace correlation.
func NewLoggingMiddleware(logger *slog.Logger) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, tag proto.Tag, msg proto.Message) proto.Message {
			opName := msg.Type().String()
			start := time.Now()
			resp := next(ctx, tag, msg)
			elapsed := time.Since(start)

			logger.LogAttrs(ctx, slog.LevelDebug, "9p request",
				slog.String("op", opName),
				slog.Duration("duration", elapsed),
				slog.Bool("error", isErrorResponse(resp)),
			)
			return resp
		}
	}
}

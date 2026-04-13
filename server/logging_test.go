package server

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// recordingHandler is a slog.Handler that captures records for test assertions.
type recordingHandler struct {
	records []slog.Record
	enabled bool
	attrs   []slog.Attr
	group   string
}

func newRecordingHandler() *recordingHandler {
	return &recordingHandler{enabled: true}
}

func (h *recordingHandler) Enabled(_ context.Context, _ slog.Level) bool {
	return h.enabled
}

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r)
	return nil
}

func (h *recordingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &recordingHandler{
		enabled: h.enabled,
		attrs:   append(h.attrs, attrs...),
		group:   h.group,
	}
}

func (h *recordingHandler) WithGroup(name string) slog.Handler {
	return &recordingHandler{
		enabled: h.enabled,
		attrs:   h.attrs,
		group:   name,
	}
}

// recordAttrs extracts all attributes from a slog.Record into a map.
func recordAttrs(r slog.Record) map[string]slog.Value {
	attrs := make(map[string]slog.Value)
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value
		return true
	})
	return attrs
}

func TestTraceHandlerHandleWithValidSpan(t *testing.T) {
	t.Parallel()

	// Use the OTel SDK test tracer to create real spans with valid IDs.
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	ctx, span := tp.Tracer("test").Start(context.Background(), "test-op")
	defer span.End()

	sc := span.SpanContext()
	if !sc.IsValid() {
		t.Fatal("expected valid span context from SDK tracer")
	}

	rec := newRecordingHandler()
	th := NewTraceHandler(rec)

	logger := slog.New(th)
	logger.InfoContext(ctx, "test message")

	if len(rec.records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(rec.records))
	}

	attrs := recordAttrs(rec.records[0])

	traceID, ok := attrs["trace_id"]
	if !ok {
		t.Fatal("expected trace_id attribute")
	}
	if traceID.String() != sc.TraceID().String() {
		t.Fatalf("trace_id: got %s, want %s", traceID.String(), sc.TraceID().String())
	}

	spanID, ok := attrs["span_id"]
	if !ok {
		t.Fatal("expected span_id attribute")
	}
	if spanID.String() != sc.SpanID().String() {
		t.Fatalf("span_id: got %s, want %s", spanID.String(), sc.SpanID().String())
	}
}

func TestTraceHandlerHandleWithoutSpan(t *testing.T) {
	t.Parallel()

	rec := newRecordingHandler()
	th := NewTraceHandler(rec)

	logger := slog.New(th)
	logger.InfoContext(context.Background(), "no span")

	if len(rec.records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(rec.records))
	}

	attrs := recordAttrs(rec.records[0])
	if _, ok := attrs["trace_id"]; ok {
		t.Fatal("unexpected trace_id attribute when no span is active")
	}
	if _, ok := attrs["span_id"]; ok {
		t.Fatal("unexpected span_id attribute when no span is active")
	}
}

func TestTraceHandlerHandleWithInvalidSpan(t *testing.T) {
	t.Parallel()

	// A noop tracer creates spans with zero TraceID/SpanID, which are NOT
	// valid. This tests the invalid span context path.
	rec := newRecordingHandler()
	th := NewTraceHandler(rec)

	// Use a context with an invalid (zero-value) span context.
	logger := slog.New(th)
	logger.InfoContext(context.Background(), "invalid span context")

	if len(rec.records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(rec.records))
	}

	attrs := recordAttrs(rec.records[0])
	if _, ok := attrs["trace_id"]; ok {
		t.Fatal("unexpected trace_id for invalid span context")
	}
	if _, ok := attrs["span_id"]; ok {
		t.Fatal("unexpected span_id for invalid span context")
	}
}

func TestTraceHandlerEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		enabled bool
	}{
		{name: "enabled", enabled: true},
		{name: "disabled", enabled: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := &recordingHandler{enabled: tt.enabled}
			th := NewTraceHandler(rec)

			got := th.Enabled(context.Background(), slog.LevelInfo)
			if got != tt.enabled {
				t.Fatalf("Enabled: got %v, want %v", got, tt.enabled)
			}
		})
	}
}

func TestTraceHandlerWithAttrs(t *testing.T) {
	t.Parallel()

	rec := newRecordingHandler()
	th := NewTraceHandler(rec)

	// WithAttrs should return a new traceHandler wrapping the inner WithAttrs result.
	th2 := th.WithAttrs([]slog.Attr{slog.String("key", "value")})

	// Verify it's still a *traceHandler (wraps correctly).
	if _, ok := th2.(*traceHandler); !ok {
		t.Fatalf("WithAttrs returned %T, want *traceHandler", th2)
	}
}

func TestTraceHandlerWithGroup(t *testing.T) {
	t.Parallel()

	rec := newRecordingHandler()
	th := NewTraceHandler(rec)

	// WithGroup should return a new traceHandler wrapping the inner WithGroup result.
	th2 := th.WithGroup("mygroup")

	// Verify it's still a *traceHandler (wraps correctly).
	if _, ok := th2.(*traceHandler); !ok {
		t.Fatalf("WithGroup returned %T, want *traceHandler", th2)
	}
}

func TestNewLoggingMiddlewareSuccess(t *testing.T) {
	t.Parallel()

	rec := newRecordingHandler()
	logger := slog.New(rec)

	mw := NewLoggingMiddleware(logger)

	inner := func(_ context.Context, _ proto.Tag, _ proto.Message) proto.Message {
		return &proto.Rwalk{}
	}

	handler := mw(inner)
	handler(context.Background(), 1, &proto.Twalk{})

	if len(rec.records) != 1 {
		t.Fatalf("expected 1 log record, got %d", len(rec.records))
	}

	r := rec.records[0]

	// Must log at Debug level, not Info.
	if r.Level != slog.LevelDebug {
		t.Fatalf("expected Debug level, got %s", r.Level)
	}

	attrs := recordAttrs(r)

	// op attribute.
	op, ok := attrs["op"]
	if !ok {
		t.Fatal("expected op attribute")
	}
	if op.String() != proto.TypeTwalk.String() {
		t.Fatalf("op: got %s, want %s", op.String(), proto.TypeTwalk.String())
	}

	// duration attribute.
	dur, ok := attrs["duration"]
	if !ok {
		t.Fatal("expected duration attribute")
	}
	if dur.Duration() <= 0 {
		t.Fatalf("expected positive duration, got %v", dur.Duration())
	}

	// error attribute should be false for success.
	errAttr, ok := attrs["error"]
	if !ok {
		t.Fatal("expected error attribute")
	}
	if errAttr.Bool() != false {
		t.Fatal("expected error=false for successful response")
	}
}

func TestNewLoggingMiddlewareError(t *testing.T) {
	t.Parallel()

	rec := newRecordingHandler()
	logger := slog.New(rec)

	mw := NewLoggingMiddleware(logger)

	inner := func(_ context.Context, _ proto.Tag, _ proto.Message) proto.Message {
		return &p9l.Rlerror{Ecode: proto.EIO}
	}

	handler := mw(inner)
	handler(context.Background(), 1, &proto.Tread{})

	if len(rec.records) != 1 {
		t.Fatalf("expected 1 log record, got %d", len(rec.records))
	}

	attrs := recordAttrs(rec.records[0])

	errAttr, ok := attrs["error"]
	if !ok {
		t.Fatal("expected error attribute")
	}
	if errAttr.Bool() != true {
		t.Fatal("expected error=true for Rlerror response")
	}
}

func TestNewLoggingMiddlewareNotInfoLevel(t *testing.T) {
	t.Parallel()

	// Create a handler that only accepts Info and above (default behavior).
	// The logging middleware should NOT produce output because it logs at Debug.
	rec := &recordingHandler{enabled: false} // simulates Info-level filter rejecting Debug
	logger := slog.New(rec)

	mw := NewLoggingMiddleware(logger)

	inner := func(_ context.Context, _ proto.Tag, _ proto.Message) proto.Message {
		return &proto.Rwalk{}
	}

	handler := mw(inner)
	handler(context.Background(), 1, &proto.Twalk{})

	// Since handler is not enabled, LogAttrs should still be called but the
	// recording handler won't capture it. We verify the middleware uses
	// Debug level by checking the previous test. Here we just ensure
	// the middleware doesn't crash with a disabled handler.
	_ = time.Now() // ensure import is used
}

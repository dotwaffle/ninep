package server

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

type failingMeterProvider struct {
	metric.MeterProvider
}

func newFailingMeterProvider() failingMeterProvider {
	return failingMeterProvider{MeterProvider: metricnoop.NewMeterProvider()}
}

func (p failingMeterProvider) Meter(name string, opts ...metric.MeterOption) metric.Meter {
	return failingMeter{Meter: p.MeterProvider.Meter(name, opts...)}
}

type failingMeter struct {
	metric.Meter
}

func (f failingMeter) Int64Counter(string, ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	return nil, errors.New("instrument rejected")
}

func TestNewReturnsInstrumentCreationError(t *testing.T) {
	t.Parallel()
	root := newDirNode(proto.QID{Type: proto.QTDIR, Path: 1})
	if _, err := New(root, WithMeter(newFailingMeterProvider())); err == nil {
		t.Fatal("New succeeded when the meter rejected an instrument")
	}
}

// otelConnPair creates a net.Pipe pair with cleanup registered on t.
func otelConnPair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	client, server = net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	return client, server
}

func TestOTelMiddlewareSpanCreation(t *testing.T) {
	t.Parallel()

	tp, exporter := NewTestTracerProvider(t)
	rootQID := proto.QID{Type: proto.QTDIR, Version: 0, Path: 1}
	root := newDirNode(rootQID)

	srv := MustNew(root,
		WithMaxMsize(65536),
		WithTracer(tp),
	)

	client, server := otelConnPair(t)
	go srv.ServeConn(t.Context(), server)

	// Handshake
	nc := client
	sendTversion(t, nc, 65536, "9P2000.L")
	_ = readRversion(t, nc)

	// Tattach
	tattach := &proto.Tattach{Fid: 0, Afid: proto.NoFid, Uname: "nobody", Aname: ""}
	sendMessage(t, nc, 1, tattach)
	_, _ = readResponse(t, nc)

	for _, span := range exporter.GetSpans() {
		if span.Name == "Tattach" {
			return
		}
	}
	t.Fatal("Tattach span not recorded")
}

func TestOTelConstructionDoesNotEmitProbeSpan(t *testing.T) {
	t.Parallel()
	tp, exporter := NewTestTracerProvider(t)
	root := newDirNode(proto.QID{Type: proto.QTDIR, Path: 1})
	if _, err := New(root, WithTracer(tp)); err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, span := range exporter.GetSpans() {
		if span.Name == "probe" {
			t.Fatal("server construction emitted a probe span")
		}
	}
}

func TestOTelMiddlewareRespectsParentSampling(t *testing.T) {
	t.Parallel()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.NeverSample())),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	root := newDirNode(proto.QID{Type: proto.QTDIR, Path: 1})
	srv := MustNew(root, WithTracer(tp))

	parentTP := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { _ = parentTP.Shutdown(context.Background()) })
	serveCtx, parent := parentTP.Tracer("test").Start(t.Context(), "parent")
	defer parent.End()

	clientConn, serverConn := otelConnPair(t)
	go srv.ServeConn(serveCtx, serverConn)
	sendTversion(t, clientConn, 65536, "9P2000.L")
	_ = readRversion(t, clientConn)
	sendMessage(t, clientConn, 1, &proto.Tattach{Fid: 0, Afid: proto.NoFid})
	_, _ = readResponse(t, clientConn)

	for _, span := range exporter.GetSpans() {
		if span.Name == "Tattach" {
			return
		}
	}
	t.Fatal("Tattach span was dropped despite its sampled parent")
}

// setupOTelTest creates a Server with OTel tracing and metrics, starts serving
// a connection, and returns the client conn plus the test span exporter and
// metric reader for assertion. The caller should negotiate version and send
// messages via the returned client.
func setupOTelTest(t *testing.T, opts ...Option) (client net.Conn, spanExporter *tracetest.InMemoryExporter, metricReader *sdkmetric.ManualReader) {
	t.Helper()

	tp, spanExporter := NewTestTracerProvider(t)
	mp, metricReader := NewTestMeterProvider(t)

	rootQID := proto.QID{Type: proto.QTDIR, Version: 0, Path: 1}
	root := newDirNode(rootQID)

	serverOpts := []Option{
		WithMaxMsize(65536),
		WithLogger(discardLogger()),
		WithTracer(tp),
		WithMeter(mp),
	}
	serverOpts = append(serverOpts, opts...)
	srv := MustNew(root, serverOpts...)

	clientConn, serverConn := otelConnPair(t)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.ServeConn(ctx, serverConn)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	return clientConn, spanExporter, metricReader
}

// collectMetrics collects all metrics from the reader and returns them.
func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	return rm
}

// findMetric searches for a metric by name across all scope metrics.
func findMetric(rm metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for _, sm := range rm.ScopeMetrics {
		for i := range sm.Metrics {
			if sm.Metrics[i].Name == name {
				return &sm.Metrics[i]
			}
		}
	}
	return nil
}

func TestOTelMiddlewareSpanAttributes(t *testing.T) {
	t.Parallel()

	client, spanExporter, _ := setupOTelTest(t)

	// Negotiate and attach.
	sendTversion(t, client, 65536, "9P2000.L")
	_ = readRversion(t, client)

	sendMessage(t, client, 1, &proto.Tattach{
		Fid:   0,
		Afid:  proto.NoFid,
		Uname: "test",
		Aname: "",
	})
	_, _ = readResponse(t, client)

	_ = client.Close()

	spans := spanExporter.GetSpans()

	// Check span attributes.
	wantAttrs := []string{"rpc.system.name", "rpc.method"}
	for _, key := range wantAttrs {
		found := false
		for _, s := range spans {
			for _, a := range s.Attributes {
				if string(a.Key) == key {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			t.Errorf("expected attribute %q in spans, not found", key)
		}
	}

	// Check rpc.system.name == "9p"
	for _, s := range spans {
		for _, a := range s.Attributes {
			if string(a.Key) == "rpc.system.name" {
				if a.Value.AsString() != "9p" {
					t.Errorf("rpc.system.name = %q, want %q", a.Value.AsString(), "9p")
				}
			}
		}
	}
}

func TestOTelMiddlewareFidAndPathAttributes(t *testing.T) {
	t.Parallel()

	client, spanExporter, _ := setupOTelTest(t, WithTracePathFilter(func(path string) (string, bool) {
		return path, true
	}))

	// Negotiate and attach.
	sendTversion(t, client, 65536, "9P2000.L")
	_ = readRversion(t, client)

	sendMessage(t, client, 1, &proto.Tattach{
		Fid:   42,
		Afid:  proto.NoFid,
		Uname: "test",
		Aname: "",
	})
	_, _ = readResponse(t, client)

	// Now send a Tclunk for fid 42, which should have the path from attach.
	sendMessage(t, client, 2, &proto.Tclunk{Fid: 42})
	_, _ = readResponse(t, client)

	_ = client.Close()

	spans := spanExporter.GetSpans()

	// Find the Tclunk span -- it should have ninep.fid=42 and ninep.path="/".
	var clunkSpan *tracetest.SpanStub
	for i := range spans {
		if spans[i].Name == "Tclunk" {
			clunkSpan = &spans[i]
			break
		}
	}
	if clunkSpan == nil {
		t.Fatal("expected span named 'Tclunk', not found")
	}

	var fidFound, pathFound, protocolFound bool
	for _, a := range clunkSpan.Attributes {
		switch string(a.Key) {
		case "ninep.fid":
			fidFound = true
			if a.Value.AsInt64() != 42 {
				t.Errorf("ninep.fid = %d, want 42", a.Value.AsInt64())
			}
		case "ninep.path":
			pathFound = true
			if a.Value.AsString() != "/" {
				t.Errorf("ninep.path = %q, want %q", a.Value.AsString(), "/")
			}
		case "ninep.protocol":
			protocolFound = true
			if a.Value.AsString() != "9P2000.L" {
				t.Errorf("ninep.protocol = %q, want %q", a.Value.AsString(), "9P2000.L")
			}
		}
	}
	if !fidFound {
		t.Error("expected ninep.fid attribute on Tclunk span")
	}
	if !pathFound {
		t.Error("expected ninep.path attribute on Tclunk span")
	}
	if !protocolFound {
		t.Error("expected ninep.protocol attribute on Tclunk span")
	}
}

func TestOTelMiddlewareOmitsPathByDefault(t *testing.T) {
	t.Parallel()
	client, spanExporter, _ := setupOTelTest(t)
	sendTversion(t, client, 65536, "9P2000.L")
	_ = readRversion(t, client)
	sendMessage(t, client, 1, &proto.Tattach{Fid: 42, Afid: proto.NoFid})
	_, _ = readResponse(t, client)
	sendMessage(t, client, 2, &proto.Tclunk{Fid: 42})
	_, _ = readResponse(t, client)
	_ = client.Close()

	for _, span := range spanExporter.GetSpans() {
		for _, attr := range span.Attributes {
			if attr.Key == "ninep.path" {
				t.Fatalf("span %q exposed ninep.path without opt-in", span.Name)
			}
		}
	}
}

func TestOTelMiddlewareRequestOutcomeMetric(t *testing.T) {
	t.Parallel()
	client, _, metricReader := setupOTelTest(t)
	sendTversion(t, client, 65536, "9P2000.L")
	_ = readRversion(t, client)
	sendMessage(t, client, 1, &proto.Tattach{Fid: 1, Afid: proto.NoFid})
	_, _ = readResponse(t, client)
	sendMessage(t, client, 2, &proto.Tclunk{Fid: 999})
	_, _ = readResponse(t, client)

	metric := findMetric(collectMetrics(t, metricReader), "ninep.server.requests")
	if metric == nil {
		t.Fatal("ninep.server.requests metric not found")
	}
	sum, ok := metric.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("requests metric data = %T, want metricdata.Sum[int64]", metric.Data)
	}
	outcomes := map[string]int64{}
	for _, point := range sum.DataPoints {
		for _, attr := range point.Attributes.ToSlice() {
			if attr.Key == "outcome" {
				outcomes[attr.Value.AsString()] += point.Value
			}
		}
	}
	if outcomes["ok"] == 0 || outcomes["error"] == 0 {
		t.Fatalf("request outcomes = %v, want both ok and error", outcomes)
	}
}

func TestOTelMiddlewareErrorSpanStatus(t *testing.T) {
	t.Parallel()

	client, spanExporter, _ := setupOTelTest(t)

	sendTversion(t, client, 65536, "9P2000.L")
	_ = readRversion(t, client)

	// Send Tclunk for a fid that was never attached -> error response.
	sendMessage(t, client, 1, &proto.Tclunk{Fid: 999})
	_, msg := readResponse(t, client)
	if _, ok := msg.(*p9l.Rlerror); !ok {
		t.Fatalf("expected Rlerror, got %T", msg)
	}

	_ = client.Close()

	spans := spanExporter.GetSpans()

	// Find the Tclunk span -- should have Error status.
	var clunkSpan *tracetest.SpanStub
	for i := range spans {
		if spans[i].Name == "Tclunk" {
			clunkSpan = &spans[i]
			break
		}
	}
	if clunkSpan == nil {
		t.Fatal("expected span named 'Tclunk', not found")
	}

	if clunkSpan.Status.Code != codes.Error {
		t.Errorf("span status = %v, want Error", clunkSpan.Status.Code)
	}
}

func TestOTelMiddlewareNonErrorSpanStatus(t *testing.T) {
	t.Parallel()

	client, spanExporter, _ := setupOTelTest(t)

	sendTversion(t, client, 65536, "9P2000.L")
	_ = readRversion(t, client)

	// Send Tattach -- should succeed.
	sendMessage(t, client, 1, &proto.Tattach{
		Fid:   0,
		Afid:  proto.NoFid,
		Uname: "test",
		Aname: "",
	})
	_, msg := readResponse(t, client)
	if _, ok := msg.(*proto.Rattach); !ok {
		t.Fatalf("expected Rattach, got %T", msg)
	}

	_ = client.Close()

	spans := spanExporter.GetSpans()

	var attachSpan *tracetest.SpanStub
	for i := range spans {
		if spans[i].Name == "Tattach" {
			attachSpan = &spans[i]
			break
		}
	}
	if attachSpan == nil {
		t.Fatal("expected span named 'Tattach', not found")
	}

	if attachSpan.Status.Code == codes.Error {
		t.Error("span status should not be Error for successful operation")
	}
}

func TestOTelMiddlewareDurationMetric(t *testing.T) {
	t.Parallel()

	client, _, metricReader := setupOTelTest(t)

	sendTversion(t, client, 65536, "9P2000.L")
	_ = readRversion(t, client)

	sendMessage(t, client, 1, &proto.Tattach{
		Fid:   0,
		Afid:  proto.NoFid,
		Uname: "test",
		Aname: "",
	})
	_, _ = readResponse(t, client)

	_ = client.Close()

	rm := collectMetrics(t, metricReader)
	m := findMetric(rm, "ninep.server.duration")
	if m == nil {
		t.Fatal("expected metric 'ninep.server.duration', not found")
	}

	hist, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("expected Histogram[float64], got %T", m.Data)
	}
	if len(hist.DataPoints) == 0 {
		t.Fatal("expected at least one data point for duration histogram")
	}
	if hist.DataPoints[0].Count == 0 {
		t.Error("expected duration histogram count > 0")
	}
}

func TestOTelMiddlewareActiveRequestsGauge(t *testing.T) {
	t.Parallel()

	client, _, metricReader := setupOTelTest(t)

	sendTversion(t, client, 65536, "9P2000.L")
	_ = readRversion(t, client)

	// Send multiple requests.
	sendMessage(t, client, 1, &proto.Tattach{
		Fid:   0,
		Afid:  proto.NoFid,
		Uname: "test",
		Aname: "",
	})
	_, _ = readResponse(t, client)

	sendMessage(t, client, 2, &proto.Tclunk{Fid: 0})
	_, _ = readResponse(t, client)

	_ = client.Close()

	rm := collectMetrics(t, metricReader)
	m := findMetric(rm, "ninep.server.active_requests")
	if m == nil {
		t.Fatal("expected metric 'ninep.server.active_requests', not found")
	}

	// After all requests complete, active_requests should be 0 (net of +1/-1 pairs).
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("expected Sum[int64], got %T", m.Data)
	}
	if len(sum.DataPoints) == 0 {
		t.Fatal("expected at least one data point for active_requests")
	}
	// Net value should be 0 since both requests have completed.
	if sum.DataPoints[0].Value != 0 {
		t.Errorf("active_requests = %d, want 0 (all requests completed)", sum.DataPoints[0].Value)
	}
}

func TestOTelMiddlewareConnectionGauge(t *testing.T) {
	t.Parallel()

	tp, _ := NewTestTracerProvider(t)
	mp, metricReader := NewTestMeterProvider(t)

	rootQID := proto.QID{Type: proto.QTDIR, Version: 0, Path: 1}
	root := newDirNode(rootQID)

	srv := MustNew(root,
		WithMaxMsize(65536),
		WithLogger(discardLogger()),
		WithTracer(tp),
		WithMeter(mp),
	)

	clientConn, serverConn := otelConnPair(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.ServeConn(ctx, serverConn)
	}()

	sendTversion(t, clientConn, 65536, "9P2000.L")
	_ = readRversion(t, clientConn)

	// Send a message and read the response to ensure the server goroutine
	// has progressed past the connection gauge increment.
	sendMessage(t, clientConn, 1, &proto.Tattach{
		Fid:   0,
		Afid:  proto.NoFid,
		Uname: "test",
		Aname: "",
	})
	_, _ = readResponse(t, clientConn)

	// While connected, the connection gauge should be 1.
	rm := collectMetrics(t, metricReader)
	m := findMetric(rm, "ninep.server.connections")
	if m == nil {
		t.Fatal("expected metric 'ninep.server.connections', not found")
	}

	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("expected Sum[int64], got %T", m.Data)
	}
	if len(sum.DataPoints) == 0 {
		t.Fatal("expected at least one data point for connections")
	}
	if sum.DataPoints[0].Value != 1 {
		t.Errorf("connections = %d, want 1 (one active connection)", sum.DataPoints[0].Value)
	}

	// Close connection and verify gauge decrements.
	_ = clientConn.Close()
	<-done

	rm2 := collectMetrics(t, metricReader)
	m2 := findMetric(rm2, "ninep.server.connections")
	if m2 == nil {
		t.Fatal("expected metric 'ninep.server.connections' after close, not found")
	}

	sum2, ok := m2.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("expected Sum[int64], got %T", m2.Data)
	}
	if len(sum2.DataPoints) == 0 {
		t.Fatal("expected at least one data point for connections after close")
	}
	if sum2.DataPoints[0].Value != 0 {
		t.Errorf("connections after close = %d, want 0", sum2.DataPoints[0].Value)
	}
}

func TestOTelMiddlewareFidCountGauge(t *testing.T) {
	t.Parallel()

	client, _, metricReader := setupOTelTest(t)

	sendTversion(t, client, 65536, "9P2000.L")
	_ = readRversion(t, client)

	// Attach creates a fid.
	sendMessage(t, client, 1, &proto.Tattach{
		Fid:   0,
		Afid:  proto.NoFid,
		Uname: "test",
		Aname: "",
	})
	_, _ = readResponse(t, client)

	// Check fid gauge is 1.
	rm := collectMetrics(t, metricReader)
	m := findMetric(rm, "ninep.server.fid.count")
	if m == nil {
		t.Fatal("expected metric 'ninep.server.fid.count', not found")
	}

	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("expected Sum[int64], got %T", m.Data)
	}
	if len(sum.DataPoints) == 0 {
		t.Fatal("expected at least one data point for fid.count")
	}
	if sum.DataPoints[0].Value != 1 {
		t.Errorf("fid.count = %d, want 1 after attach", sum.DataPoints[0].Value)
	}

	// Clunk removes the fid.
	sendMessage(t, client, 2, &proto.Tclunk{Fid: 0})
	_, _ = readResponse(t, client)

	rm2 := collectMetrics(t, metricReader)
	m2 := findMetric(rm2, "ninep.server.fid.count")
	if m2 == nil {
		t.Fatal("expected metric 'ninep.server.fid.count' after clunk, not found")
	}

	sum2, ok := m2.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("expected Sum[int64], got %T", m2.Data)
	}
	if len(sum2.DataPoints) == 0 {
		t.Fatal("expected at least one data point for fid.count after clunk")
	}
	if sum2.DataPoints[0].Value != 0 {
		t.Errorf("fid.count after clunk = %d, want 0", sum2.DataPoints[0].Value)
	}
}

// TestOTelMiddlewareFidCountGauge_Xattrwalk verifies that a successful
// Txattrwalk increments the fid.count gauge for its new fid, mirroring
// TestOTelMiddlewareFidCountGauge's Tattach/Tclunk coverage. Txattrwalk
// always allocates a new fid via c.fids.add on success (get, list, and
// RawXattrer modes alike), so the gauge must track it the same way Twalk's
// clone path does.
func TestOTelMiddlewareFidCountGauge_Xattrwalk(t *testing.T) {
	t.Parallel()

	tp, _ := NewTestTracerProvider(t)
	mp, metricReader := NewTestMeterProvider(t)

	root := &getterOnlyFile{value: []byte("v")}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)

	srv := MustNew(root,
		WithMaxMsize(65536),
		WithLogger(discardLogger()),
		WithTracer(tp),
		WithMeter(mp),
	)

	client, server := otelConnPair(t)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.ServeConn(ctx, server)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	sendTversion(t, client, 65536, "9P2000.L")
	_ = readRversion(t, client)

	sendMessage(t, client, 1, &proto.Tattach{Fid: 0, Afid: proto.NoFid, Uname: "test"})
	_, _ = readResponse(t, client)

	sendMessage(t, client, 2, &p9l.Txattrwalk{Fid: 0, NewFid: 1, Name: "user.test"})
	_, msg := readResponse(t, client)
	if _, ok := msg.(*p9l.Rxattrwalk); !ok {
		t.Fatalf("expected Rxattrwalk, got %T: %+v", msg, msg)
	}

	rm := collectMetrics(t, metricReader)
	m := findMetric(rm, "ninep.server.fid.count")
	if m == nil {
		t.Fatal("expected metric 'ninep.server.fid.count', not found")
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("expected Sum[int64], got %T", m.Data)
	}
	if len(sum.DataPoints) == 0 {
		t.Fatal("expected at least one data point for fid.count")
	}
	if sum.DataPoints[0].Value != 2 {
		t.Errorf("fid.count = %d, want 2 (attach fid 0 + xattrwalk fid 1)", sum.DataPoints[0].Value)
	}
}

func TestOTelMiddlewareNoProviderNoOverhead(t *testing.T) {
	t.Parallel()

	// Server without WithTracer/WithMeter should NOT have OTel middleware.
	rootQID := proto.QID{Type: proto.QTDIR, Version: 0, Path: 1}
	root := newDirNode(rootQID)

	srv := MustNew(root, WithMaxMsize(65536), WithLogger(discardLogger()))

	// The middlewares slice should be empty (no OTel middleware auto-added).
	if len(srv.middlewares) != 0 {
		t.Errorf("expected 0 middlewares without OTel config, got %d", len(srv.middlewares))
	}
}

func TestOTelMiddlewareRequestResponseSize(t *testing.T) {
	t.Parallel()

	client, _, metricReader := setupOTelTest(t)

	sendTversion(t, client, 65536, "9P2000.L")
	_ = readRversion(t, client)

	sendMessage(t, client, 1, &proto.Tattach{
		Fid:   0,
		Afid:  proto.NoFid,
		Uname: "test",
		Aname: "",
	})
	_, _ = readResponse(t, client)

	_ = client.Close()

	rm := collectMetrics(t, metricReader)

	reqSize := findMetric(rm, "ninep.server.request.size")
	if reqSize == nil {
		t.Fatal("expected metric 'ninep.server.request.size', not found")
	}
	// Only the Tattach reaches the middleware (Tversion short-circuits
	// before dispatch). Its .L body is fid[4] + afid[4] + uname[2+4] +
	// aname[2+0] + n_uname[4] = 20 bytes, which equals the wire frame size
	// minus the 7-byte header. This pins the read-loop-recorded size
	// against the value the old ByteCounter re-encode produced.
	reqSum, ok := reqSize.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("request.size: expected Sum[int64], got %T", reqSize.Data)
	}
	if len(reqSum.DataPoints) == 0 {
		t.Fatal("request.size: expected at least one data point")
	}
	if got := reqSum.DataPoints[0].Value; got != 20 {
		t.Errorf("request.size = %d, want 20 (Tattach body length)", got)
	}

	respSize := findMetric(rm, "ninep.server.response.size")
	if respSize == nil {
		t.Fatal("expected metric 'ninep.server.response.size', not found")
	}
}

func TestWithTracerAndWithMeterOptions(t *testing.T) {
	t.Parallel()

	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(t.Context()) })

	mp := sdkmetric.NewMeterProvider()
	t.Cleanup(func() { _ = mp.Shutdown(t.Context()) })

	rootQID := proto.QID{Type: proto.QTDIR, Version: 0, Path: 1}
	root := newDirNode(rootQID)

	srv := MustNew(root,
		WithTracer(tp),
		WithMeter(mp),
	)

	if srv.tracerProvider == nil {
		t.Error("expected tracerProvider to be set")
	}
	if srv.meterProvider == nil {
		t.Error("expected meterProvider to be set")
	}
}

func TestServerTelemetryConfiguration(t *testing.T) {
	t.Parallel()

	rootQID := proto.QID{Type: proto.QTDIR, Version: 0, Path: 1}

	cases := []struct {
		name          string
		opts          []Option
		wantInstalled bool
	}{
		{
			name: "no options",
		},
		{
			name: "both_noop_package_providers",
			opts: []Option{
				WithTracer(tracenoop.NewTracerProvider()),
				WithMeter(metricnoop.NewMeterProvider()),
			},
			wantInstalled: true,
		},
		{
			name: "both_global_defaults_pre_sdk",
			opts: []Option{
				WithTracer(otel.GetTracerProvider()),
				WithMeter(otel.GetMeterProvider()),
			},
			wantInstalled: true,
		},
		{
			name: "both_sdk_providers",
			opts: []Option{
				WithTracer(sdktrace.NewTracerProvider()),
				WithMeter(sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewManualReader()))),
			},
			wantInstalled: true,
		},
		{
			name: "real_tracer_noop_meter",
			opts: []Option{
				WithTracer(sdktrace.NewTracerProvider()),
				WithMeter(metricnoop.NewMeterProvider()),
			},
			wantInstalled: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := newDirNode(rootQID)
			srv := MustNew(root, append([]Option{WithLogger(discardLogger())}, tc.opts...)...)
			installed := srv.otelCore != nil && srv.connInst != nil
			if installed != tc.wantInstalled {
				t.Errorf("telemetry installed = %v, want %v", installed, tc.wantInstalled)
			}
		})
	}
}

func TestOTelExplicitNoopProvidersInstall(t *testing.T) {
	t.Parallel()

	rootQID := proto.QID{Type: proto.QTDIR, Version: 0, Path: 1}
	root := newDirNode(rootQID)

	srv := MustNew(root,
		WithLogger(discardLogger()),
		WithTracer(tracenoop.NewTracerProvider()),
		WithMeter(metricnoop.NewMeterProvider()),
	)

	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	c := newConn(srv, server)
	if c.otelInst == nil {
		t.Fatal("explicit no-op providers did not install telemetry middleware")
	}
}

// TestOTelPartialNoopInstalls verifies the partially-noop install path:
// when EITHER a real tracer OR a real meter is configured (but not both),
// the OTel middleware MUST still be installed. The install predicate
// is OR, not AND.
func TestOTelPartialNoopInstalls(t *testing.T) {
	t.Parallel()

	rootQID := proto.QID{Type: proto.QTDIR, Version: 0, Path: 1}

	cases := []struct {
		name string
		opts []Option
	}{
		{
			name: "real_tracer_noop_meter",
			opts: []Option{
				WithTracer(sdktrace.NewTracerProvider()),
				WithMeter(metricnoop.NewMeterProvider()),
			},
		},
		{
			name: "noop_tracer_real_meter",
			opts: []Option{
				WithTracer(tracenoop.NewTracerProvider()),
				// A ManualReader supplies a real metric consumer.
				WithMeter(sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewManualReader()))),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := newDirNode(rootQID)
			srv := MustNew(root, append([]Option{WithLogger(discardLogger())}, tc.opts...)...)

			client, server := net.Pipe()
			t.Cleanup(func() {
				_ = client.Close()
				_ = server.Close()
			})
			c := newConn(srv, server)
			if c.otelInst == nil {
				t.Errorf("c.otelInst = nil, want non-nil (partial config MUST install middleware)")
			}
		})
	}
}

// TestOTelAbnormalEventsHandlerPanic asserts the abnormal_events counter
// records a handler panic with the reason attribute. The panic path is the
// only abnormal event triggerable without waiting out a multi-second
// deadline; the other reasons share the same recordAbnormalEvent plumbing.
func TestOTelAbnormalEventsHandlerPanic(t *testing.T) {
	t.Parallel()

	mp, metricReader := NewTestMeterProvider(t)

	root := &panicNode{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)

	cp := newConnPair(t, root, WithMeter(mp))
	defer cp.close(t)
	cp.attach(t, 1, 0, "user", "")

	// Twalk calls panicNode.Lookup; the server recovers and replies EIO.
	sendMessage(t, cp.client, 10, &proto.Twalk{
		Fid:    0,
		NewFid: 1,
		Names:  []string{"anything"},
	})
	_, msg := readResponse(t, cp.client)
	isError(t, msg, proto.EIO)

	rm := collectMetrics(t, metricReader)
	m := findMetric(rm, "ninep.server.abnormal_events")
	if m == nil {
		t.Fatal("expected metric 'ninep.server.abnormal_events', not found")
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("expected Sum[int64], got %T", m.Data)
	}
	if len(sum.DataPoints) != 1 {
		t.Fatalf("data points = %d, want 1", len(sum.DataPoints))
	}
	dp := sum.DataPoints[0]
	if dp.Value != 1 {
		t.Errorf("abnormal_events = %d, want 1", dp.Value)
	}
	reason, ok := dp.Attributes.Value(attribute.Key("reason"))
	if !ok || reason.AsString() != reasonHandlerPanic {
		t.Errorf("reason attribute = %q (present=%v), want %q",
			reason.AsString(), ok, reasonHandlerPanic)
	}
}

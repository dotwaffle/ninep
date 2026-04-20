package server

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/internal/otelutil"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

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

	tp, exporter := otelutil.NewTestTracerProvider(t)
	rootQID := proto.QID{Type: proto.QTDIR, Version: 0, Path: 1}
	root := newDirNode(rootQID)

	srv := New(root,
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

	// Verify spans
	spans := exporter.GetSpans()
	if len(spans) < 2 {
		t.Errorf("got %d spans, want >= 2", len(spans))
	}
}

// setupOTelTest creates a Server with OTel tracing and metrics, starts serving
// a connection, and returns the client conn plus the test span exporter and
// metric reader for assertion. The caller should negotiate version and send
// messages via the returned client.
func setupOTelTest(t *testing.T) (client net.Conn, spanExporter *tracetest.InMemoryExporter, metricReader *sdkmetric.ManualReader) {
	t.Helper()

	tp, spanExporter := otelutil.NewTestTracerProvider(t)
	mp, metricReader := otelutil.NewTestMeterProvider(t)

	rootQID := proto.QID{Type: proto.QTDIR, Version: 0, Path: 1}
	root := newDirNode(rootQID)

	srv := New(root,
		WithMaxMsize(65536),
		WithLogger(discardLogger()),
		WithTracer(tp),
		WithMeter(mp),
	)

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
	time.Sleep(100 * time.Millisecond)

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

	client, spanExporter, _ := setupOTelTest(t)

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
	time.Sleep(100 * time.Millisecond)

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
	time.Sleep(100 * time.Millisecond)

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
	time.Sleep(100 * time.Millisecond)

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
	time.Sleep(100 * time.Millisecond)

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
	time.Sleep(100 * time.Millisecond)

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

	tp, _ := otelutil.NewTestTracerProvider(t)
	mp, metricReader := otelutil.NewTestMeterProvider(t)

	rootQID := proto.QID{Type: proto.QTDIR, Version: 0, Path: 1}
	root := newDirNode(rootQID)

	srv := New(root,
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

func TestOTelMiddlewareNoProviderNoOverhead(t *testing.T) {
	t.Parallel()

	// Server without WithTracer/WithMeter should NOT have OTel middleware.
	rootQID := proto.QID{Type: proto.QTDIR, Version: 0, Path: 1}
	root := newDirNode(rootQID)

	srv := New(root, WithMaxMsize(65536), WithLogger(discardLogger()))

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
	time.Sleep(100 * time.Millisecond)

	rm := collectMetrics(t, metricReader)

	reqSize := findMetric(rm, "ninep.server.request.size")
	if reqSize == nil {
		t.Fatal("expected metric 'ninep.server.request.size', not found")
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

	srv := New(root,
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

// TestServerNoopDetection verifies probeOTelProviders correctly populates
// s.tracerRecording and s.meterEnabled across the matrix of configurations
// the server may encounter. Covers PERF-06 SC-1.
func TestServerNoopDetection(t *testing.T) {
	t.Parallel()

	rootQID := proto.QID{Type: proto.QTDIR, Version: 0, Path: 1}

	cases := []struct {
		name             string
		opts             []Option
		wantRecording    bool
		wantMeterEnabled bool
	}{
		{
			name:             "no_options_both_nil",
			opts:             nil,
			wantRecording:    false,
			wantMeterEnabled: false,
		},
		{
			name: "both_noop_package_providers",
			opts: []Option{
				WithTracer(tracenoop.NewTracerProvider()),
				WithMeter(metricnoop.NewMeterProvider()),
			},
			wantRecording:    false,
			wantMeterEnabled: false,
		},
		{
			name: "both_global_defaults_pre_sdk",
			opts: []Option{
				WithTracer(otel.GetTracerProvider()),
				WithMeter(otel.GetMeterProvider()),
			},
			wantRecording:    false,
			wantMeterEnabled: false,
		},
		{
			name: "both_sdk_providers",
			opts: []Option{
				WithTracer(sdktrace.NewTracerProvider()),
				// A MeterProvider with NO reader reports Enabled()==false
				// because there is no consumer to process measurements --
				// OTel's SDK treats a reader-less provider as effectively
				// a noop. Wire a ManualReader so the probe sees the SDK
				// as enabled (the real deployment case where a reader is
				// always attached).
				WithMeter(sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewManualReader()))),
			},
			wantRecording:    true,
			wantMeterEnabled: true,
		},
		{
			name: "real_tracer_noop_meter",
			opts: []Option{
				WithTracer(sdktrace.NewTracerProvider()),
				WithMeter(metricnoop.NewMeterProvider()),
			},
			wantRecording:    true,
			wantMeterEnabled: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := newDirNode(rootQID)
			srv := New(root, append([]Option{WithLogger(discardLogger())}, tc.opts...)...)
			if srv.tracerRecording != tc.wantRecording {
				t.Errorf("tracerRecording = %v, want %v", srv.tracerRecording, tc.wantRecording)
			}
			if srv.meterEnabled != tc.wantMeterEnabled {
				t.Errorf("meterEnabled = %v, want %v", srv.meterEnabled, tc.wantMeterEnabled)
			}
		})
	}
}

// TestOTelNoopShortCircuit verifies the short-circuit path: when both
// providers are noop, no OTel middleware is installed on a new conn's
// handler chain and c.otelInst stays nil. Covers PERF-06 SC-4 and D-04.
func TestOTelNoopShortCircuit(t *testing.T) {
	t.Parallel()

	rootQID := proto.QID{Type: proto.QTDIR, Version: 0, Path: 1}
	root := newDirNode(rootQID)

	srv := New(root,
		WithLogger(discardLogger()),
		WithTracer(tracenoop.NewTracerProvider()),
		WithMeter(metricnoop.NewMeterProvider()),
	)

	// Probe results must be false -- both providers are noop.
	if srv.tracerRecording {
		t.Errorf("tracerRecording = true, want false (noop tracer)")
	}
	if srv.meterEnabled {
		t.Errorf("meterEnabled = true, want false (noop meter)")
	}

	// Build a conn via newConn (the install site) and verify c.otelInst
	// is nil -- the connOTelInstruments are only created inside the
	// install gate, so a nil otelInst proves the gate did not fire.
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	c := newConn(srv, server)
	if c.otelInst != nil {
		t.Errorf("c.otelInst = %+v, want nil (short-circuit should skip install)", c.otelInst)
	}
}

// TestOTelPartialNoopInstalls verifies the partially-noop install path:
// when EITHER a real tracer OR a real meter is configured (but not both),
// the OTel middleware MUST still be installed per D-06. The install
// predicate is OR, not AND. Covers PERF-06 SC-6.
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
				// A reader-less sdkmetric.MeterProvider reports
				// Enabled()==false, making it indistinguishable from a
				// noop meter. Wire a ManualReader so the probe sees the
				// SDK meter as enabled.
				WithMeter(sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewManualReader()))),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := newDirNode(rootQID)
			srv := New(root, append([]Option{WithLogger(discardLogger())}, tc.opts...)...)

			client, server := net.Pipe()
			t.Cleanup(func() {
				_ = client.Close()
				_ = server.Close()
			})
			c := newConn(srv, server)
			if c.otelInst == nil {
				t.Errorf("c.otelInst = nil, want non-nil (partial config MUST install middleware per D-06)")
			}
		})
	}
}

package client

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/internal/otelutil"
	"github.com/dotwaffle/ninep/server"
	"github.com/dotwaffle/ninep/server/memfs"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestClientOTel_Tracing(t *testing.T) {
	t.Parallel()

	tp, exporter := otelutil.NewTestTracerProvider(t)

	// Setup server
	gen := new(server.QIDGenerator)
	root := memfs.NewDir(gen).
		AddFile("hello.txt", []byte("hello"))
	srv := server.New(root)
	cliNC, srvNC := net.Pipe()
	defer func() { _ = cliNC.Close() }()
	defer func() { _ = srvNC.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go srv.ServeConn(ctx, srvNC)

	// Dial with tracer
	cli, err := Dial(ctx, cliNC, WithTracer(tp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cli.Close() }()

	// Initial Tversion is not instrumented (runs inside Dial).
	// We check for subsequent ops.

	if _, err := cli.Attach(ctx, "nobody", ""); err != nil {
		t.Fatal(err)
	}

	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("expected spans, got 0")
	}

	// Verify Tattach span
	var attachSpan *tracetest.SpanStub
	for i := range spans {
		if spans[i].Name == "Tattach" {
			attachSpan = &spans[i]
			break
		}
	}
	if attachSpan == nil {
		t.Fatal("Tattach span not found")
	}

	attrs := make(map[string]any)
	for _, a := range attachSpan.Attributes {
		attrs[string(a.Key)] = a.Value.AsInterface()
	}

	if attrs["rpc.system.name"] != "9p" {
		t.Errorf("rpc.system.name = %v, want 9p", attrs["rpc.system.name"])
	}
	if attrs["rpc.method"] != "Tattach" {
		t.Errorf("rpc.method = %v, want Tattach", attrs["rpc.method"])
	}
	if attrs["ninep.fid"] == nil {
		t.Error("ninep.fid attribute missing")
	}
}

func TestClientOTel_Metrics(t *testing.T) {
	t.Parallel()

	mp, reader := otelutil.NewTestMeterProvider(t)

	// Setup server
	gen := new(server.QIDGenerator)
	root := memfs.NewDir(gen).
		AddFile("hello.txt", []byte("hello"))
	srv := server.New(root)
	cliNC, srvNC := net.Pipe()
	defer func() { _ = cliNC.Close() }()
	defer func() { _ = srvNC.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go srv.ServeConn(ctx, srvNC)

	// Dial with meter
	cli, err := Dial(ctx, cliNC, WithMeter(mp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cli.Close() }()

	if _, err := cli.Attach(ctx, "nobody", ""); err != nil {
		t.Fatal(err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatal(err)
	}

	m := otelutil.GetMetric(rm, "ninep.client.duration")
	if m == nil {
		t.Fatal("ninep.client.duration metric missing")
	}

	reqSize := otelutil.GetMetric(rm, "ninep.client.request.size")
	if reqSize == nil {
		t.Fatal("ninep.client.request.size metric missing")
	}

	active := otelutil.GetMetric(rm, "ninep.client.active_requests")
	if active == nil {
		t.Fatal("ninep.client.active_requests metric missing")
	}
}

func TestClientOTel_ErrorSpan(t *testing.T) {
	t.Parallel()

	tp, exporter := otelutil.NewTestTracerProvider(t)

	// Setup server
	gen := new(server.QIDGenerator)
	root := memfs.NewDir(gen)
	srv := server.New(root)
	cliNC, srvNC := net.Pipe()
	defer func() { _ = cliNC.Close() }()
	defer func() { _ = srvNC.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go srv.ServeConn(ctx, srvNC)

	cli, err := Dial(ctx, cliNC, WithTracer(tp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cli.Close() }()

	// Try to walk to non-existent file to trigger error.
	rootF, err := cli.Attach(ctx, "nobody", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = rootF.Walk(ctx, []string{"ghost"})
	if err == nil {
		t.Fatal("expected walk error, got nil")
	}

	spans := exporter.GetSpans()
	var walkSpan *tracetest.SpanStub
	for i := range spans {
		if spans[i].Name == "Twalk" {
			walkSpan = &spans[i]
			break
		}
	}
	if walkSpan == nil {
		t.Fatal("Twalk span not found")
	}

	if walkSpan.Status.Code != codes.Error {
		t.Errorf("span status = %v, want Error", walkSpan.Status.Code)
	}
}

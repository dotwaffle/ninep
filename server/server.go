package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Server serves the 9P protocol over network connections. Create with New.
type Server struct {
	root           Node
	maxMsize       uint32
	maxInflight    int
	maxConnections int64         // 0 = unlimited
	connCount      atomic.Int64  // active connections (internal bookkeeping)
	maxFids        int           // 0 = unlimited (per-connection cap)
	idleTimeout    time.Duration // 0 = no timeout (GO-SEC-1)
	logger         *slog.Logger
	anames         map[string]Node
	attacher       Attacher
	middlewares    []Middleware
	tracerProvider trace.TracerProvider
	meterProvider  metric.MeterProvider
	otelInst       *serverOTelInstruments // server-level metrics (nil if no MeterProvider)
}

// New creates a Server rooted at the given Node. Options configure behavior.
// The root must implement NodeLookuper for walk resolution.
func New(root Node, opts ...Option) *Server {
	s := &Server{
		root:        root,
		maxMsize:    1024 * 1024, // 1MiB default
		maxInflight: 64,
		logger:      slog.New(NewTraceHandler(slog.Default().Handler())),
		// idleTimeout: 0 (zero value = no timeout)
	}
	for _, opt := range opts {
		opt(s)
	}
	s.otelInst = newServerOTelInstruments(s.meterProvider) // nil if no MeterProvider
	return s
}

// Serve accepts connections from ln and serves each one in a new goroutine.
// It blocks until the context is cancelled or the listener returns an error.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		nc, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("accept: %w", err)
		}
		wg.Go(func() {
			s.ServeConn(ctx, nc)
		})
	}
}

// ServeConn serves a single 9P connection. It blocks until the connection is
// closed or the context is cancelled.
//
// When the server has a WithMaxConnections limit configured and the limit is
// reached, ServeConn closes nc immediately, logs a warning, increments the
// ninep.server.connections_rejected counter, and returns without serving.
func (s *Server) ServeConn(ctx context.Context, nc net.Conn) {
	if s.maxConnections > 0 {
		if s.connCount.Add(1) > s.maxConnections {
			s.connCount.Add(-1)
			s.logger.Warn("connection rejected: max connections reached",
				slog.Int64("max", s.maxConnections),
				slog.String("remote", nc.RemoteAddr().String()),
			)
			s.otelInst.recordConnectionRejected()
			_ = nc.Close()
			return
		}
		defer s.connCount.Add(-1)
	}
	c := newConn(s, nc)
	c.serve(ctx)
}

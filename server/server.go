package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/dotwaffle/ninep/proto"
)

// Accept backoff bounds for transient errors (fd exhaustion). Mirrors the
// capped exponential backoff in net/http.Server.Serve.
const (
	acceptBackoffStart    = 5 * time.Millisecond
	acceptBackoffMax      = 1 * time.Second
	defaultMaxConnections = 1024
	defaultMaxFids        = 4096
	defaultIdleTimeout    = 2 * time.Minute
)

// Server serves the 9P protocol over network connections. Create with New.
type Server struct {
	configErr        error
	root             Node
	maxMsize         uint32
	maxInflight      int
	maxConnections   int64         // 0 = unlimited
	connCount        atomic.Int64  // active connections (internal bookkeeping)
	maxFids          int           // 0 = unlimited (per-connection cap)
	idleTimeout      time.Duration // 0 disables idle timeouts.
	handshakeTimeout time.Duration // bounds the initial version handshake when idleTimeout is 0
	drainTimeout     time.Duration // bounds inflight drain during cleanup and re-negotiation
	logger           *slog.Logger
	anames           map[string]Node
	attacher         Attacher
	middlewares      []Middleware
	requestLogging   bool // install per-request logging using the wrapped s.logger
	tracerProvider   trace.TracerProvider
	meterProvider    metric.MeterProvider
	tracePathFilter  TracePathFilter
	otelInst         *serverOTelInstruments // server-level metrics (nil if no MeterProvider)
	otelCore         *otelCore
	connInst         *connOTelInstruments
}

// New creates a Server rooted at the given Node. Options configure behavior.
// The root must implement NodeLookuper for walk resolution.
func New(root Node, opts ...Option) (*Server, error) {
	s := &Server{
		root:             root,
		maxMsize:         1024 * 1024, // 1MiB default
		maxInflight:      64,
		maxConnections:   defaultMaxConnections,
		maxFids:          defaultMaxFids,
		idleTimeout:      defaultIdleTimeout,
		logger:           slog.New(NewTraceHandler(slog.Default().Handler())),
		handshakeTimeout: defaultHandshakeTimeout,
		drainTimeout:     defaultDrainTimeout,
		// idleTimeout: 0 (zero value = no timeout)
	}
	for _, opt := range opts {
		opt(s)
	}
	if err := s.validate(); err != nil {
		return nil, err
	}

	telemetryConfigured := s.tracerProvider != nil || s.meterProvider != nil
	if telemetryConfigured {
		if s.tracerProvider == nil {
			s.tracerProvider = tracenoop.NewTracerProvider()
		}
		if s.meterProvider == nil {
			s.meterProvider = metricnoop.NewMeterProvider()
		}
	}

	var err error
	s.otelInst, err = newServerOTelInstruments(s.meterProvider)
	if err != nil {
		return nil, fmt.Errorf("server: initialize telemetry: %w", err)
	}
	if telemetryConfigured {
		// Build the shared middleware core and conn/fid gauges once here
		// rather than per connection: instrument creation takes the SDK
		// registry mutex and the attribute cache is ~30 map entries.
		s.otelCore, err = newOTelCore(s.tracerProvider, s.meterProvider)
		if err != nil {
			return nil, fmt.Errorf("server: initialize request telemetry: %w", err)
		}
		s.connInst, err = newConnOTelInstruments(s.meterProvider)
		if err != nil {
			return nil, fmt.Errorf("server: initialize connection telemetry: %w", err)
		}
	}
	return s, nil
}

// MustNew is like New but panics if the configuration is invalid. It is
// intended for package-level setup and tests where invalid static options are
// programmer errors.
func MustNew(root Node, opts ...Option) *Server {
	s, err := New(root, opts...)
	if err != nil {
		panic(err)
	}
	return s
}

func (s *Server) validate() error {
	if s.configErr != nil {
		return s.configErr
	}
	if s.root == nil && s.attacher == nil && len(s.anames) == 0 {
		return errors.New("server: root, aname, or attacher is required")
	}
	for name, root := range s.anames {
		if root == nil {
			return fmt.Errorf("server: aname %q has a nil root", name)
		}
	}
	if s.maxMsize < minMsize || s.maxMsize > proto.MaxMessageSize {
		return fmt.Errorf("server: max msize %d outside [%d, %d]", s.maxMsize, minMsize, proto.MaxMessageSize)
	}
	if s.maxInflight < 1 {
		return fmt.Errorf("server: max inflight must be positive: %d", s.maxInflight)
	}
	if s.maxConnections < 0 {
		return fmt.Errorf("server: max connections must not be negative: %d", s.maxConnections)
	}
	if s.maxFids < 0 {
		return fmt.Errorf("server: max fids must not be negative: %d", s.maxFids)
	}
	if s.idleTimeout < 0 {
		return fmt.Errorf("server: idle timeout must not be negative: %s", s.idleTimeout)
	}
	if s.handshakeTimeout <= 0 {
		return fmt.Errorf("server: handshake timeout must be positive: %s", s.handshakeTimeout)
	}
	if s.drainTimeout <= 0 {
		return fmt.Errorf("server: drain timeout must be positive: %s", s.drainTimeout)
	}
	if s.logger == nil {
		return errors.New("server: logger must not be nil")
	}
	return nil
}

func (s *Server) setConfigError(err error) {
	if s.configErr == nil {
		s.configErr = err
	}
}

// Serve accepts connections from ln and serves each one in a new goroutine.
// It blocks until the context is cancelled or the listener returns an error.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	var wg sync.WaitGroup
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = ln.Close()
		case <-done:
		}
	}()
	defer func() {
		close(done)
		wg.Wait()
	}()

	var backoff time.Duration
	for {
		nc, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Transient fd exhaustion (EMFILE/ENFILE) must not tear down the
			// whole server: internal/poll does not retry these, so a single
			// occurrence would otherwise stop accepting every future
			// connection. Back off and keep accepting so existing peers
			// survive and new ones resume once descriptors free up.
			if isTransientAcceptError(err) {
				if backoff == 0 {
					backoff = acceptBackoffStart
				} else {
					backoff = min(backoff*2, acceptBackoffMax)
				}
				s.logger.Warn("accept: transient error, backing off",
					slog.Duration("delay", backoff),
					slog.Any("error", err),
				)
				select {
				case <-time.After(backoff):
				case <-ctx.Done():
					return ctx.Err()
				}
				continue
			}
			return fmt.Errorf("accept: %w", err)
		}
		backoff = 0
		wg.Go(func() {
			s.ServeConn(ctx, nc)
		})
	}
}

// isTransientAcceptError reports whether an Accept error is transient resource
// exhaustion (EMFILE/ENFILE) that should be retried rather than treated as
// fatal. ECONNABORTED is already retried inside internal/poll, and a
// deadline-less listener never returns a timeout, so those need no handling
// here.
func isTransientAcceptError(err error) bool {
	return errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE)
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

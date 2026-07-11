package server

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"time"
)

// Option configures a Server. Pass to New.
type Option func(*Server)

// WithMaxMsize sets the maximum message size the server will accept during
// version negotiation. Default: 1048576 (1 MiB, matches the Linux kernel's
// silent msize cap). New rejects values outside the protocol allocation
// bounds.
func WithMaxMsize(msize uint32) Option {
	return func(s *Server) { s.maxMsize = msize }
}

// WithMaxInflight sets the maximum number of concurrent in-flight requests
// per connection. New rejects values less than 1. Default: 64.
func WithMaxInflight(n int) Option {
	return func(s *Server) { s.maxInflight = n }
}

// WithMaxConnections sets the maximum number of concurrent connections the
// server will serve. When the limit is reached, ServeConn closes the new
// connection immediately, logs a warning, and increments the
// ninep.server.connections_rejected OTel counter. Zero disables the limit;
// negative values are invalid. Default: 1024.
func WithMaxConnections(n int) Option {
	return func(s *Server) { s.maxConnections = int64(n) }
}

// WithMaxFids sets the maximum number of concurrent fids the server will
// allow per connection. When the cap is reached, fid-creating operations
// (Tattach, Twalk, Txattrwalk) return EMFILE. The cap check runs inside
// fidTable.add under the write lock, making enforcement race-free. Values
// zero disables the limit; negative values are invalid. Default: 4096.
func WithMaxFids(n int) Option {
	return func(s *Server) { s.maxFids = n }
}

// WithLogger sets the structured logger for the server. The handler is
// automatically wrapped with trace ID correlation (see NewTraceHandler).
// Default: slog.Default() with trace correlation. A nil logger is invalid.
func WithLogger(logger *slog.Logger) Option {
	return func(s *Server) {
		if logger == nil {
			s.logger = nil
		} else {
			s.logger = slog.New(NewTraceHandler(logger.Handler()))
		}
	}
}

// WithRequestLogging installs per-request Debug logging that reuses the
// server's own logger, the one configured by WithLogger (or the default)
// and already wrapped for trace-ID correlation. Unlike
// WithMiddleware(NewLoggingMiddleware(logger)), which logs through a
// caller-supplied logger that may lack trace correlation, this routes
// request logs through the wrapped, per-connection logger so they carry
// trace_id and span_id. The request-logging middleware runs inside any
// OTel span, so the span context is populated when each line is emitted.
func WithRequestLogging() Option {
	return func(s *Server) { s.requestLogging = true }
}

// WithAnames sets a map of aname strings to root nodes for vhost-style
// attach dispatch. When set, Tattach uses the aname field to select the
// root node. An empty aname falls back to the default root. The map is
// cloned, so later mutation by the caller cannot race attach handling.
func WithAnames(m map[string]Node) Option {
	return func(s *Server) { s.anames = maps.Clone(m) }
}

// AttachRequest contains the identity and tree claims carried by Tattach.
// Ninep does not authenticate these values; authorization-sensitive servers
// must establish peer identity at the transport layer.
type AttachRequest struct {
	Uname string
	Aname string
	UID   uint32
}

// Attacher provides full-control attach handling. When set via WithAttacher,
// it overrides the default root-node and aname-dispatch behavior.
type Attacher interface {
	// Attach resolves the root node for a connection from the peer's
	// unauthenticated Tattach claims.
	Attach(ctx context.Context, request AttachRequest) (Node, error)
}

// WithAttacher sets a custom Attacher that handles all Tattach requests.
// When set, it takes precedence over both the default root node and any
// aname map configured via WithAnames.
func WithAttacher(a Attacher) Option {
	return func(s *Server) {
		s.attacher = a
		if a == nil {
			s.setConfigError(errors.New("server: attacher must not be nil"))
		}
	}
}

// WithDrainTimeout sets the maximum time the server waits for inflight
// request handlers to finish during connection cleanup and mid-session
// Tversion re-negotiation. Handlers that ignore context cancellation past
// the deadline are logged and orphaned (cleanup) or cause the connection to
// be closed (re-negotiation). New rejects non-positive values. Default: 5s.
func WithDrainTimeout(d time.Duration) Option {
	return func(s *Server) { s.drainTimeout = d }
}

// WithIdleTimeout sets the per-connection idle timeout. When d > 0, the server
// resets read and write deadlines on the underlying net.Conn before each I/O
// operation. A connection that sees no activity for the duration is closed.
// Default: 2m. Set zero only for a trusted transport whose lifecycle is
// bounded externally.
//
// Set this whenever peers are untrusted. Responses are written inline under
// a per-connection write mutex, so a client that stops reading (a full
// socket buffer, deliberate or not) wedges the writing handler and, as
// other handlers finish and queue on the mutex, eventually every dispatcher
// on that connection. With no write deadline the wedge holds fids, buffers,
// and worker slots forever; with an idle timeout the stalled write errors
// out and the connection is torn down.
func WithIdleTimeout(d time.Duration) Option {
	return func(s *Server) { s.idleTimeout = d }
}

// WithTrustedNetwork disables the default connection, fid, and established
// I/O bounds. Use it only when access and connection lifetimes are enforced by
// a trusted transport or an outer server.
func WithTrustedNetwork() Option {
	return func(s *Server) {
		s.maxConnections = 0
		s.maxFids = 0
		s.idleTimeout = 0
	}
}

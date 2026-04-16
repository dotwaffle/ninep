package server

import (
	"context"
	"log/slog"
	"time"
)

// Option configures a Server. Pass to New.
type Option func(*Server)

// WithMaxMsize sets the maximum message size the server will accept during
// version negotiation. Default: 1048576 (1 MiB, matches the Linux kernel's
// silent msize cap).
func WithMaxMsize(msize uint32) Option {
	return func(s *Server) { s.maxMsize = msize }
}

// WithMaxInflight sets the maximum number of concurrent in-flight requests
// per connection. Values less than 1 are clamped to 1. Default: 64.
func WithMaxInflight(n int) Option {
	return func(s *Server) {
		if n < 1 {
			n = 1
		}
		s.maxInflight = n
	}
}

// WithMaxConnections sets the maximum number of concurrent connections the
// server will serve. When the limit is reached, ServeConn closes the new
// connection immediately, logs a warning, and increments the
// ninep.server.connections_rejected OTel counter. Values less than 1 disable
// the limit. Default: 0 (no limit).
func WithMaxConnections(n int) Option {
	return func(s *Server) {
		if n < 1 {
			n = 0
		}
		s.maxConnections = int64(n)
	}
}

// WithMaxFids sets the maximum number of concurrent fids the server will
// allow per connection. When the cap is reached, fid-creating operations
// (Tattach, Twalk, Txattrwalk) return EMFILE. The cap check runs inside
// fidTable.add under the write lock, making enforcement race-free. Values
// less than 1 disable the limit. Default: 0 (no limit).
func WithMaxFids(n int) Option {
	return func(s *Server) {
		if n < 1 {
			n = 0
		}
		s.maxFids = n
	}
}

// WithLogger sets the structured logger for the server. The handler is
// automatically wrapped with trace ID correlation (see NewTraceHandler).
// Default: slog.Default() with trace correlation.
func WithLogger(logger *slog.Logger) Option {
	return func(s *Server) {
		s.logger = slog.New(NewTraceHandler(logger.Handler()))
	}
}

// WithAnames sets a map of aname strings to root nodes for vhost-style
// attach dispatch. When set, Tattach uses the aname field to select the
// root node. An empty aname falls back to the default root.
func WithAnames(m map[string]Node) Option {
	return func(s *Server) { s.anames = m }
}

// Attacher provides full-control attach handling. When set via WithAttacher,
// it overrides the default root-node and aname-dispatch behavior.
type Attacher interface {
	// Attach resolves the root node for a connection given the uname and
	// aname from Tattach.
	Attach(ctx context.Context, uname, aname string) (Node, error)
}

// WithAttacher sets a custom Attacher that handles all Tattach requests.
// When set, it takes precedence over both the default root node and any
// aname map configured via WithAnames.
func WithAttacher(a Attacher) Option {
	return func(s *Server) { s.attacher = a }
}

// WithIdleTimeout sets the per-connection idle timeout. When d > 0, the server
// resets read and write deadlines on the underlying net.Conn before each I/O
// operation. A connection that sees no activity for the duration is closed.
// Default: 0 (no timeout -- caller manages via net.Conn wrapping if needed).
func WithIdleTimeout(d time.Duration) Option {
	return func(s *Server) { s.idleTimeout = d }
}

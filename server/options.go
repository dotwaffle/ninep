package server

import (
	"context"
	"log/slog"
	"time"
)

// Option configures a Server. Pass to New.
type Option func(*Server)

// WithMaxMsize sets the maximum message size the server will accept during
// version negotiation. Default: 131072 (128KB).
func WithMaxMsize(msize uint32) Option {
	return func(s *Server) { s.maxMsize = msize }
}

// WithMaxInflight sets the maximum number of concurrent in-flight requests
// per connection. Default: 64.
func WithMaxInflight(n int) Option {
	return func(s *Server) { s.maxInflight = n }
}

// WithLogger sets the structured logger for the server. Default: slog.Default().
func WithLogger(logger *slog.Logger) Option {
	return func(s *Server) { s.logger = logger }
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

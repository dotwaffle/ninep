package client

import (
	"context"
	"net"
	"sync"
	"time"
)

// Session manages a stateful 9P connection that handles automatic
// reconnection with backoff.
type Session struct {
	mu     sync.Mutex
	conn   *Conn
	dialer func(ctx context.Context) (net.Conn, error)
	opts   []Option

	onReconnect func(context.Context, *Conn) error
}

// SessionOption configures a Session.
type SessionOption func(*Session)

// WithOnReconnect sets a callback to be invoked every time a new
// connection is established (including the initial connection).
// If the callback returns an error, the connection is closed and
// reconnection is retried.
func WithOnReconnect(fn func(context.Context, *Conn) error) SessionOption {
	return func(s *Session) {
		s.onReconnect = fn
	}
}

// NewSession returns a new Session that uses the provided dialer to
// establish connections.
func NewSession(dialer func(ctx context.Context) (net.Conn, error), opts ...Option) *Session {
	return &Session{
		dialer: dialer,
		opts:   opts,
	}
}

// NewSessionWithOptions returns a new Session with the provided dialer
// and session options.
func NewSessionWithOptions(dialer func(ctx context.Context) (net.Conn, error), opts []Option, sopts ...SessionOption) *Session {
	s := &Session{
		dialer: dialer,
		opts:   opts,
	}
	for _, opt := range sopts {
		opt(s)
	}
	return s
}

// Conn returns a live *Conn. If the current connection is nil or closed,
// it re-establishes the connection using the Session's dialer.
//
// Conn is safe for concurrent use. Multiple goroutines calling Conn
// simultaneously when a connection is needed will only trigger one
// dialer call; the others will block until the connection is ready.
//
// On dialer or handshake failure, Conn retries with exponential backoff
// (default 10/20/40/80/160/320/500ms cap) until a connection is
// established or the provided context is cancelled.
func (s *Session) Conn(ctx context.Context) (*Conn, error) {
	// Fast path: return existing conn if it's healthy.
	s.mu.Lock()
	if s.conn != nil && !s.conn.isClosed() {
		defer s.mu.Unlock()
		return s.conn, nil
	}
	s.mu.Unlock()

	// Slow path: redial loop with backoff.
	for i := 0; ; i++ {
		s.mu.Lock()

		// Check again under lock in case someone else dialled while we waited.
		if s.conn != nil && !s.conn.isClosed() {
			s.mu.Unlock()
			return s.conn, nil
		}

		nc, err := s.dialer(ctx)
		if err == nil {
			var c *Conn
			c, err = Dial(ctx, nc, s.opts...)
				if err == nil {
					if s.onReconnect != nil {
						if err = s.onReconnect(ctx, c); err != nil {
							_ = c.Close()
						}
					}
					if err == nil {
						s.conn = c
						s.mu.Unlock()
						return c, nil
					}
				} else {
					_ = nc.Close()
				}
		}

		s.mu.Unlock()

		// If we reached here, dial or Dial failed. Apply backoff.
		// Honor context cancellation.
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		t := time.NewTimer(backoffFor(defaultLockBackoff, i))
		select {
		case <-t.C:
			// Continue to next retry.
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		}
	}
}

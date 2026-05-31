package client

import (
	"context"
	"net"
	"sync"
	"time"
)

// Session manages a stateful 9P connection that handles automatic
// reconnection with backoff.
//
// Silent peer death (a half-open connection with no FIN/RST) is detected by
// TCP keepalive for *net.TCPConn transports (see Dial); on other transports it
// surfaces only when a request times out, after which Conn redials.
type Session struct {
	mu     sync.Mutex
	conn   *Conn
	dialer func(ctx context.Context) (net.Conn, error)
	opts   []Option
	// dialing is non-nil while one goroutine is (re)dialing; it is closed when
	// that attempt sequence ends so waiters can re-check state without each
	// holding the dial. Lets concurrent Conn callers wait cancellably instead
	// of blocking on the dial under s.mu.
	dialing chan struct{}
	closed  bool

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
	for {
		s.mu.Lock()
		switch {
		case s.closed:
			s.mu.Unlock()
			return nil, ErrSessionClosed
		case s.conn != nil && !s.conn.isClosed():
			c := s.conn
			s.mu.Unlock()
			return c, nil
		case s.dialing != nil:
			// Another goroutine is dialing. Wait for it cancellably (the dial
			// itself runs without s.mu held), then re-check.
			done := s.dialing
			s.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		// Become the dialer.
		done := make(chan struct{})
		s.dialing = done
		s.mu.Unlock()

		// dialWithBackoff runs the dialer, Dial, and onReconnect WITHOUT
		// holding s.mu, so concurrent callers neither block on the lock nor
		// miss their own cancellation, and a failed onReconnect's Close does
		// not stall other callers.
		c, err := s.dialWithBackoff(ctx)

		s.mu.Lock()
		s.dialing = nil
		close(done)
		switch {
		case s.closed:
			// Session was closed while we dialed; discard the new conn.
			s.mu.Unlock()
			if err == nil {
				_ = c.Close()
			}
			return nil, ErrSessionClosed
		case err == nil:
			s.conn = c
			s.mu.Unlock()
			return c, nil
		default:
			s.mu.Unlock()
			return nil, err
		}
	}
}

// dialWithBackoff dials and runs onReconnect, retrying with exponential
// backoff until success or ctx cancellation. It holds no Session lock.
func (s *Session) dialWithBackoff(ctx context.Context) (*Conn, error) {
	for i := 0; ; i++ {
		c, err := s.dialOnce(ctx)
		if err == nil {
			return c, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		t := time.NewTimer(backoffFor(defaultLockBackoff, i))
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		}
	}
}

// dialOnce performs a single connection attempt: dialer, Dial, then
// onReconnect. On any failure it closes whatever it opened and returns the
// error. Holds no Session lock, so the blocking Close on an onReconnect
// failure does not serialize other callers.
func (s *Session) dialOnce(ctx context.Context) (*Conn, error) {
	nc, err := s.dialer(ctx)
	if err != nil {
		return nil, err
	}
	c, err := Dial(ctx, nc, s.opts...)
	if err != nil {
		_ = nc.Close()
		return nil, err
	}
	if s.onReconnect != nil {
		if err := s.onReconnect(ctx, c); err != nil {
			_ = c.Close()
			return nil, err
		}
	}
	return c, nil
}

// Close shuts the Session down: it closes the current connection, if any, and
// makes future Conn calls return ErrSessionClosed. Safe to call more than once
// and safe for concurrent use. A connection handed out by Conn before Close is
// the caller's to keep using until they close it.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	c := s.conn
	s.conn = nil
	s.mu.Unlock()

	if c != nil {
		return c.Close()
	}
	return nil
}

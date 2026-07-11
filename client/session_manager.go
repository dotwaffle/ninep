package client

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"net"
	"sync"
	"time"
)

// defaultDialBackoff is the retry cadence for Session reconnection. It
// starts low so a transient blip (server restart, dropped socket) is
// retried almost immediately, then doubles up to a 5s cap so a peer that
// stays down is probed at a sustainable rate rather than hammered at the
// sub-second cadence a lock poll wants. Each sleep gets up to +50%
// random jitter (see dialJitter) so a fleet of clients that lost the
// same server does not reconnect in lockstep.
//
// Override via [WithReconnectBackoff].
var defaultDialBackoff = []time.Duration{
	10 * time.Millisecond,
	20 * time.Millisecond,
	40 * time.Millisecond,
	80 * time.Millisecond,
	160 * time.Millisecond,
	320 * time.Millisecond,
	640 * time.Millisecond,
	1280 * time.Millisecond,
	2560 * time.Millisecond,
	5 * time.Second,
}

// dialJitter widens d by a uniform random extension in [0, d/2]. Jitter
// only ever lengthens the sleep, so schedule entries remain lower bounds
// (tests asserting minimum elapsed time stay valid) while simultaneous
// reconnecters decorrelate.
func dialJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	return d + rand.N(d/2+1)
}

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
	// closeCh is closed exactly once by Close. dialWithBackoff selects on it
	// so Close stops an in-progress retry loop even when the caller passed a
	// context that never cancels (e.g. context.Background()).
	closeCh chan struct{}
	closed  bool

	onReconnect func(context.Context, *Conn) error
	// backoff is the reconnect retry schedule (never empty; defaults to
	// defaultDialBackoff). Immutable after construction.
	backoff []time.Duration
	// logger receives a Debug record per failed dial attempt. Never nil;
	// defaults to slog.Default(). Immutable after construction.
	logger *slog.Logger
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

// WithReconnectBackoff overrides the retry schedule used when a dial or
// handshake fails (default [defaultDialBackoff]: 10ms doubling to a 5s
// cap). After the last entry the cadence stays at that entry; up to +50%
// random jitter is added to every sleep. An empty schedule is ignored.
// The slice is copied, so callers may reuse theirs after the call.
func WithReconnectBackoff(schedule []time.Duration) SessionOption {
	return func(s *Session) {
		if len(schedule) == 0 {
			return
		}
		s.backoff = append([]time.Duration(nil), schedule...)
	}
}

// WithSessionLogger sets the structured logger the Session uses to report
// failed dial attempts (one Debug record per failure). A nil logger is
// ignored - the existing logger (by default [slog.Default]) is kept. This
// is distinct from [WithLogger], which configures the per-Conn logger.
func WithSessionLogger(logger *slog.Logger) SessionOption {
	return func(s *Session) {
		if logger != nil {
			s.logger = logger
		}
	}
}

// NewSession returns a new Session that uses the provided dialer to
// establish connections.
func NewSession(dialer func(ctx context.Context) (net.Conn, error), opts ...Option) *Session {
	return &Session{
		dialer:  dialer,
		opts:    opts,
		closeCh: make(chan struct{}),
		backoff: defaultDialBackoff,
		logger:  slog.Default(),
	}
}

// NewSessionWithOptions returns a new Session with the provided dialer
// and session options.
func NewSessionWithOptions(dialer func(ctx context.Context) (net.Conn, error), opts []Option, sopts ...SessionOption) *Session {
	s := &Session{
		dialer:  dialer,
		opts:    opts,
		closeCh: make(chan struct{}),
		backoff: defaultDialBackoff,
		logger:  slog.Default(),
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
// (default 10ms doubling to a 5s cap, plus jitter; see
// [WithReconnectBackoff]) until a connection is established or the
// provided context is cancelled.
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
		// Close stops the loop even when ctx never cancels (e.g.
		// context.Background()), so a permanently failing dialer cannot pin
		// this goroutine for the process lifetime.
		select {
		case <-s.closeCh:
			return nil, ErrSessionClosed
		default:
		}
		d := dialJitter(backoffFor(s.backoff, i))
		s.logger.Debug("client: session dial failed",
			slog.Int("attempt", i+1),
			slog.Duration("backoff", d),
			slog.Any("error", err),
		)
		t := time.NewTimer(d)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-s.closeCh:
			t.Stop()
			return nil, ErrSessionClosed
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
	close(s.closeCh) // stops an in-progress dialWithBackoff retry loop
	c := s.conn
	s.conn = nil
	s.mu.Unlock()

	if c != nil {
		return c.Close()
	}
	return nil
}

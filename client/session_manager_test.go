package client

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

func runMockVersionServer(t *testing.T, srvNC net.Conn) {
	t.Helper()
	go func() {
		// Read Tversion
		var sizeBuf [4]byte
		if _, err := io.ReadFull(srvNC, sizeBuf[:]); err != nil {
			return
		}
		size := binary.LittleEndian.Uint32(sizeBuf[:])
		body := make([]byte, int(size)-4)
		if _, err := io.ReadFull(srvNC, body); err != nil {
			return
		}

		// Write Rversion
		resp := proto.Rversion{Msize: 65536, Version: "9P2000.L"}
		if err := p9l.Encode(srvNC, proto.NoTag, &resp); err != nil {
			return
		}

		// Sink
		sink := make([]byte, 4096)
		for {
			if _, err := srvNC.Read(sink); err != nil {
				return
			}
		}
	}()
}

func TestSession_Basic(t *testing.T) {
	var dials int
	dialer := func(ctx context.Context) (net.Conn, error) {
		dials++
		c1, s1 := net.Pipe()
		runMockVersionServer(t, s1)
		return c1, nil
	}

	s := NewSession(dialer)
	ctx := context.Background()

	// Test 2: Session.Conn() returns the initial connection.
	c1, err := s.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn() failed: %v", err)
	}
	if dials != 1 {
		t.Errorf("Expected 1 dial, got %d", dials)
	}

	// Calling Conn again should return the same connection
	c2, err := s.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn() second call failed: %v", err)
	}
	if c1 != c2 {
		t.Errorf("Expected same connection, got different ones")
	}
	if dials != 1 {
		t.Errorf("Expected still 1 dial, got %d", dials)
	}

	// Test 3: If Conn is closed, next Session.Conn() call redials and returns a new Conn.
	_ = c1.Close()

	c3, err := s.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn() after close failed: %v", err)
	}
	if c3 == c1 {
		t.Errorf("Expected new connection, got the old closed one")
	}
	if dials != 2 {
		t.Errorf("Expected 2 dials, got %d", dials)
	}
}

func TestSession_Concurrent(t *testing.T) {
	var dials int
	var mu sync.Mutex
	dialer := func(ctx context.Context) (net.Conn, error) {
		mu.Lock()
		dials++
		mu.Unlock()
		c1, s1 := net.Pipe()
		runMockVersionServer(t, s1)
		return c1, nil
	}

	s := NewSession(dialer)
	ctx := context.Background()

	const numGoroutines = 10
	var wg sync.WaitGroup
	wg.Add(numGoroutines)
	conns := make([]*Conn, numGoroutines)
	for i := range numGoroutines {
		go func(i int) {
			defer wg.Done()
			c, err := s.Conn(ctx)
			if err != nil {
				t.Errorf("Goroutine %d failed: %v", i, err)
				return
			}
			conns[i] = c
		}(i)
	}
	wg.Wait()

	if dials != 1 {
		t.Errorf("Expected 1 dial for concurrent initial calls, got %d", dials)
	}
	for i := 1; i < numGoroutines; i++ {
		if conns[i] != conns[0] {
			t.Errorf("Goroutine %d got different connection", i)
		}
	}
}

func TestSession_Conn_Backoff(t *testing.T) {
	var dials int
	dialer := func(ctx context.Context) (net.Conn, error) {
		dials++
		if dials < 3 {
			return nil, net.ErrClosed // Simulate failure
		}
		c1, s1 := net.Pipe()
		runMockVersionServer(t, s1)
		return c1, nil
	}

	s := NewSession(dialer)
	ctx := context.Background()

	start := time.Now()
	c1, err := s.Conn(ctx)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Conn() failed: %v", err)
	}
	if dials != 3 {
		t.Errorf("Expected 3 dials, got %d", dials)
	}
	// 2 failures = 10ms + 20ms = 30ms total backoff expected.
	if elapsed < 30*time.Millisecond {
		t.Errorf("Expected at least 30ms backoff, got %v", elapsed)
	}
	if c1 == nil {
		t.Fatal("Conn() returned nil")
	}
}

func TestSession_Conn_Cancel(t *testing.T) {
	dialer := func(ctx context.Context) (net.Conn, error) {
		return nil, net.ErrClosed // Always fail
	}

	s := NewSession(dialer)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := s.Conn(ctx)
	if err == nil {
		t.Fatal("Conn() succeeded, want error")
	}
	if ctx.Err() == nil {
		t.Error("Expected context error")
	}
}

func TestSession_OnReconnect(t *testing.T) {
	var dials int
	dialer := func(ctx context.Context) (net.Conn, error) {
		dials++
		c1, s1 := net.Pipe()
		runMockVersionServer(t, s1)
		return c1, nil
	}

	var reconns int
	onReconnect := func(ctx context.Context, c *Conn) error {
		reconns++
		if reconns == 1 {
			return net.ErrClosed // Fail the first one
		}
		return nil
	}

	s := NewSessionWithOptions(dialer, nil, WithOnReconnect(onReconnect))
	ctx := context.Background()

	c1, err := s.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn() failed: %v", err)
	}
	if dials != 2 {
		t.Errorf("Expected 2 dials (1 fail in callback), got %d", dials)
	}
	if reconns != 2 {
		t.Errorf("Expected 2 reconn calls, got %d", reconns)
	}
	if c1 == nil {
		t.Fatal("Conn() returned nil")
	}
}

func TestSession_Flaky(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping flaky stress test in short mode")
	}

	var mu sync.Mutex
	var dials int
	dialer := func(ctx context.Context) (net.Conn, error) {
		mu.Lock()
		defer mu.Unlock()
		dials++

		// 1/3 fail immediately
		if dials%3 == 0 {
			return nil, net.ErrClosed
		}

		c1, s1 := net.Pipe()

		// 1/3 close immediately after handshake
		if dials%3 == 1 {
			go func() {
				// Read Tversion
				var sizeBuf [4]byte
				if _, err := io.ReadFull(s1, sizeBuf[:]); err != nil {
					return
				}
				size := binary.LittleEndian.Uint32(sizeBuf[:])
				body := make([]byte, int(size)-4)
				if _, err := io.ReadFull(s1, body); err != nil {
					return
				}
				// Write Rversion
				resp := proto.Rversion{Msize: 65536, Version: "9P2000.L"}
				_ = p9l.Encode(s1, proto.NoTag, &resp)
				// Close immediately
				_ = s1.Close()
			}()
			return c1, nil
		}

		// 1/3 succeed
		runMockVersionServer(t, s1)
		return c1, nil
	}

	s := NewSession(dialer)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const numGoroutines = 20
	var wg sync.WaitGroup
	wg.Add(numGoroutines)
	for i := range numGoroutines {
		go func(i int) {
			defer wg.Done()
			for j := range 5 {
				_, err := s.Conn(ctx)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					t.Errorf("Goroutine %d, attempt %d failed: %v", i, j, err)
					return
				}
				// We don't strictly require isClosed to be false here because it could
				// have closed immediately after Conn() returned.
				time.Sleep(10 * time.Millisecond)
			}
		}(i)
	}
	wg.Wait()
}

// TestSession_Close asserts Close shuts the session down: the live conn is
// closed and future Conn calls return ErrSessionClosed. Close is idempotent.
func TestSession_Close(t *testing.T) {
	t.Parallel()
	dialer := func(_ context.Context) (net.Conn, error) {
		c1, s1 := net.Pipe()
		runMockVersionServer(t, s1)
		return c1, nil
	}
	s := NewSession(dialer)

	c, err := s.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !c.isClosed() {
		t.Error("Session.Close did not close the live connection")
	}
	if _, err := s.Conn(context.Background()); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("Conn after Close: err = %v, want ErrSessionClosed", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestSession_ConnCancellableDuringSlowDial asserts a Conn caller can cancel
// while another goroutine is mid-dial, instead of blocking on the session lock
// for the whole dial.
func TestSession_ConnCancellableDuringSlowDial(t *testing.T) {
	t.Parallel()

	var once sync.Once
	dialStarted := make(chan struct{})
	releaseDial := make(chan struct{})
	dialer := func(ctx context.Context) (net.Conn, error) {
		once.Do(func() { close(dialStarted) })
		select {
		case <-releaseDial:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		c1, s1 := net.Pipe()
		runMockVersionServer(t, s1)
		return c1, nil
	}
	s := NewSession(dialer)
	t.Cleanup(func() { _ = s.Close() })

	// Goroutine A becomes the dialer and blocks inside the dialer.
	go func() { _, _ = s.Conn(context.Background()) }()
	<-dialStarted

	// Goroutine B must observe its own cancellation while A is dialing.
	ctxB, cancelB := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancelB()
	start := time.Now()
	if _, err := s.Conn(ctxB); err == nil {
		t.Fatal("B's Conn returned success; expected cancellation while A dials")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("B blocked %v on the slow dial; should cancel promptly", elapsed)
	}
	close(releaseDial)
}

// TestSession_CloseStopsRetryLoop verifies Close ends an in-progress dial
// retry loop even when the caller passed a context that never cancels. A
// permanently failing dialer would otherwise pin the dial goroutine for the
// life of the process.
func TestSession_CloseStopsRetryLoop(t *testing.T) {
	t.Parallel()

	dialed := make(chan struct{}, 1)
	dialer := func(_ context.Context) (net.Conn, error) {
		select {
		case dialed <- struct{}{}:
		default:
		}
		return nil, errors.New("dial always fails")
	}
	s := NewSession(dialer)

	connErr := make(chan error, 1)
	go func() {
		// context.Background never cancels, so only Close can end the loop.
		_, err := s.Conn(context.Background())
		connErr <- err
	}()

	// Wait until the dialer has run at least once (loop is spinning).
	select {
	case <-dialed:
	case <-time.After(2 * time.Second):
		t.Fatal("dialer never called")
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-connErr:
		if !errors.Is(err, ErrSessionClosed) {
			t.Fatalf("Conn after Close = %v, want ErrSessionClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Conn did not return after Close; dial goroutine leaked")
	}
}

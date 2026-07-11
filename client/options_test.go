package client

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
)

func TestDefaultConfig(t *testing.T) {
	t.Parallel()
	c := newConfig()
	if c.msize != 1<<20 {
		t.Fatalf("default msize = %d, want %d", c.msize, 1<<20)
	}
	if c.maxInflight != 64 {
		t.Fatalf("default maxInflight = %d, want 64", c.maxInflight)
	}
	if c.logger == nil {
		t.Fatalf("default logger is nil")
	}
}

func TestWithMsize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   uint32
		want uint32
	}{
		{"minimum", minMsize, minMsize},
		{"default", 1 << 20, 1 << 20},
		{"small", 8192, 8192},
		{"large", 4 << 20, 4 << 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := newConfig()
			WithMsize(tt.in)(c)
			if c.msize != tt.want {
				t.Fatalf("msize = %d, want %d", c.msize, tt.want)
			}
		})
	}
}

func TestWithMaxInflight(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   int
	}{
		{"one-accepted", 1},
		{"default", 64},
		{"upper-boundary", 32766},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := newConfig()
			WithMaxInflight(tt.in)(c)
			if c.err != nil {
				t.Fatalf("WithMaxInflight(%d): %v", tt.in, c.err)
			}
			if c.maxInflight != tt.in {
				t.Fatalf("maxInflight = %d, want %d", c.maxInflight, tt.in)
			}
		})
	}
	for _, invalid := range []int{0, -5, 32767, 1 << 20} {
		c := newConfig()
		WithMaxInflight(invalid)(c)
		if c.err == nil {
			t.Errorf("WithMaxInflight(%d) accepted invalid value", invalid)
		}
	}
}

// Ensure the NoTag exclusion bound is computed from proto.NoTag, not a
// hardcoded duplicate. The highest possible flush mirror tag
// (flushTagBit | maxMaxInflight) must stay strictly below NoTag so a
// Tflush can never masquerade as a Tversion exchange.
func TestWithMaxInflight_NoTagBoundTracksProto(t *testing.T) {
	t.Parallel()
	if got, limit := flushTagBit|maxMaxInflight, int(uint16(proto.NoTag)); got >= limit {
		t.Fatalf("flushTagBit|maxMaxInflight = %d, must be < NoTag (%d)", got, limit)
	}
	if maxMaxInflight != flushTagBit-2 {
		t.Fatalf("maxMaxInflight = %d, want %d (= flushTagBit-2, leaving the top mirror slot clear of NoTag)", maxMaxInflight, flushTagBit-2)
	}
}

func TestWithLogger(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	custom := slog.New(slog.NewTextHandler(&buf, nil))

	c := newConfig()
	WithLogger(custom)(c)
	if c.logger != custom {
		t.Fatalf("WithLogger did not install custom logger")
	}
}

func TestWithLogger_NilIsInvalid(t *testing.T) {
	t.Parallel()
	c := newConfig()
	WithLogger(nil)(c)
	if c.err == nil {
		t.Fatal("WithLogger(nil) did not record a configuration error")
	}
}

// TestWithRequestTimeout_Default verifies the default config carries a zero
// requestTimeout value - "infinite wait / Linux v9fs parity".
func TestWithRequestTimeout_Default(t *testing.T) {
	t.Parallel()
	c := newConfig()
	if c.requestTimeout != 0 {
		t.Fatalf("default requestTimeout = %v, want 0 (infinite)", c.requestTimeout)
	}
}

// TestWithRequestTimeout_Sets verifies positive d values flow through to the
// config field verbatim.
func TestWithRequestTimeout_Sets(t *testing.T) {
	t.Parallel()
	c := newConfig()
	WithRequestTimeout(500 * time.Millisecond)(c)
	if c.requestTimeout != 500*time.Millisecond {
		t.Fatalf("requestTimeout = %v, want 500ms", c.requestTimeout)
	}
}

// TestWithRequestTimeout_Zero_Resets verifies explicit WithRequestTimeout(0)
// resets a previously-set timeout back to "infinite". Zero is a valid caller
// intent, not a "use the default" marker.
func TestWithRequestTimeout_Zero_Resets(t *testing.T) {
	t.Parallel()
	c := newConfig()
	WithRequestTimeout(100 * time.Millisecond)(c)
	WithRequestTimeout(0)(c)
	if c.requestTimeout != 0 {
		t.Fatalf("requestTimeout after 0-reset = %v, want 0", c.requestTimeout)
	}
}

// TestWithRequestTimeout_Negative verifies negative durations are invalid.
func TestWithRequestTimeout_Negative(t *testing.T) {
	t.Parallel()
	c := newConfig()
	WithRequestTimeout(-1 * time.Second)(c)
	if c.err == nil {
		t.Fatal("negative request timeout did not record a configuration error")
	}
}

func TestInvalidOptionalProvidersAndSchedule(t *testing.T) {
	t.Parallel()
	for name, opt := range map[string]Option{
		"empty lock schedule": WithLockPollSchedule(nil),
		"negative lock delay": WithLockPollSchedule([]time.Duration{-time.Millisecond}),
		"nil tracer":          WithTracer(nil),
		"nil meter":           WithMeter(nil),
	} {
		c := newConfig()
		opt(c)
		if c.err == nil {
			t.Errorf("%s did not record a configuration error", name)
		}
	}
}

// TestConn_OpCtx_DefaultInfinite verifies opCtx returns the parent ctx
// unchanged when requestTimeout is zero -- no hidden allocation, no hidden
// deadline, caller gets what they passed in.
func TestConn_OpCtx_DefaultInfinite(t *testing.T) {
	t.Parallel()
	c := &Conn{} // requestTimeout zero-value = 0 (infinite)
	ctx, cancel := c.opCtx(context.Background())
	defer cancel()
	if _, ok := ctx.Deadline(); ok {
		t.Fatalf("opCtx(Background) with requestTimeout=0 returned ctx with Deadline; want no deadline")
	}
}

// TestConn_OpCtx_Timeout verifies opCtx derives a context.WithTimeout when
// requestTimeout is positive. The deadline is approximately now + d (within
// a generous tolerance for test timing).
func TestConn_OpCtx_Timeout(t *testing.T) {
	t.Parallel()
	const timeout = 50 * time.Millisecond
	c := &Conn{requestTimeout: timeout}
	before := time.Now()
	ctx, cancel := c.opCtx(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatalf("opCtx with requestTimeout=%v returned ctx without Deadline", timeout)
	}
	// Deadline must sit within [before+timeout, before+timeout+5ms]. The
	// 5ms upper bound is generous for slow CI hosts but tight enough to
	// catch "forgot to apply the timeout at all" bugs.
	min := before.Add(timeout)
	max := before.Add(timeout + 5*time.Millisecond)
	if deadline.Before(min) || deadline.After(max) {
		t.Fatalf("opCtx deadline = %v; want within [%v, %v]", deadline, min, max)
	}
}

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
		{"zero-passes-through", 0, 0},
		{"default", 1 << 20, 1 << 20},
		{"small", 8192, 8192},
		{"large", 4 << 20, 4 << 20},
	}
	for _, tt := range tests {
		tt := tt
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
		want int
	}{
		{"zero-clamps-to-1", 0, 1},
		{"negative-clamps-to-1", -5, 1},
		{"one-accepted", 1, 1},
		{"default", 64, 64},
		{"upper-boundary", 65534, 65534},
		{"above-upper-clamps", 65535, 65534},
		{"way-above-clamps", 1 << 20, 65534},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := newConfig()
			WithMaxInflight(tt.in)(c)
			if c.maxInflight != tt.want {
				t.Fatalf("maxInflight = %d, want %d", c.maxInflight, tt.want)
			}
		})
	}
}

// Ensure the NoTag exclusion bound is computed from proto.NoTag, not a hardcoded
// duplicate. If proto.NoTag ever changes, the clamp must track it.
func TestWithMaxInflight_NoTagBoundTracksProto(t *testing.T) {
	t.Parallel()
	want := int(uint16(proto.NoTag)) - 1
	if maxMaxInflight != want {
		t.Fatalf("maxMaxInflight = %d, want %d (= uint16(proto.NoTag)-1)", maxMaxInflight, want)
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

func TestWithLogger_NilIsNoOp(t *testing.T) {
	t.Parallel()
	c := newConfig()
	orig := c.logger
	// Must not panic and must preserve the existing (default) logger.
	WithLogger(nil)(c)
	if c.logger != orig {
		t.Fatalf("WithLogger(nil) changed the logger; expected no-op")
	}
}

// TestWithRequestTimeout_Default verifies the default config carries a zero
// requestTimeout value — "infinite wait / Linux v9fs parity" per D-22 /
// Pitfall 9.
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

// TestWithRequestTimeout_Negative verifies negative durations are coerced to
// 0 (infinite). Rationale: callers MAY accidentally pass a subtraction
// overflow or a "duration until deadline" that has already passed; treating
// those as "no timeout" matches Linux v9fs parity and is safer than a
// pathological sub-millisecond timeout.
func TestWithRequestTimeout_Negative(t *testing.T) {
	t.Parallel()
	c := newConfig()
	WithRequestTimeout(-1 * time.Second)(c)
	if c.requestTimeout != 0 {
		t.Fatalf("requestTimeout after negative input = %v, want 0 (coerced)", c.requestTimeout)
	}
}

// TestConn_OpCtx_DefaultInfinite verifies opCtx returns the parent ctx
// unchanged when requestTimeout is zero — no hidden allocation, no hidden
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

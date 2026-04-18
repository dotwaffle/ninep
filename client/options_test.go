package client

import (
	"bytes"
	"log/slog"
	"testing"

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

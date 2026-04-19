package clienttest

import (
	"context"
	"testing"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/server"
)

// TestWithServerOpts verifies that WithServerOpts appends server options
// to the internal config in call order.
func TestWithServerOpts(t *testing.T) {
	t.Parallel()
	cfg := newConfig()
	a := server.WithMaxMsize(1024)
	b := server.WithMaxMsize(2048)
	WithServerOpts(a, b)(cfg)
	if got, want := len(cfg.serverOpts), 2; got != want {
		t.Fatalf("serverOpts len = %d, want %d", got, want)
	}
}

// TestWithServerOpts_Appends verifies repeated calls append rather than
// replace (two separate WithServerOpts applications concatenate).
func TestWithServerOpts_Appends(t *testing.T) {
	t.Parallel()
	cfg := newConfig()
	WithServerOpts(server.WithMaxMsize(1))(cfg)
	WithServerOpts(server.WithMaxMsize(2), server.WithMaxMsize(3))(cfg)
	if got, want := len(cfg.serverOpts), 3; got != want {
		t.Fatalf("serverOpts len = %d, want %d", got, want)
	}
}

// TestWithClientOpts verifies that WithClientOpts appends client options
// to the internal config in call order.
func TestWithClientOpts(t *testing.T) {
	t.Parallel()
	cfg := newConfig()
	a := client.WithMsize(1024)
	b := client.WithMsize(2048)
	WithClientOpts(a, b)(cfg)
	if got, want := len(cfg.clientOpts), 2; got != want {
		t.Fatalf("clientOpts len = %d, want %d", got, want)
	}
}

// TestWithClientOpts_Appends verifies repeated calls append rather than
// replace.
func TestWithClientOpts_Appends(t *testing.T) {
	t.Parallel()
	cfg := newConfig()
	WithClientOpts(client.WithMsize(1))(cfg)
	WithClientOpts(client.WithMsize(2), client.WithMsize(3))(cfg)
	if got, want := len(cfg.clientOpts), 3; got != want {
		t.Fatalf("clientOpts len = %d, want %d", got, want)
	}
}

// TestWithMsize_SetsBoth verifies that WithMsize(n) populates BOTH
// serverMsize and clientMsize (the D-08 "sets both" contract).
func TestWithMsize_SetsBoth(t *testing.T) {
	t.Parallel()
	cfg := newConfig()
	WithMsize(32768)(cfg)
	if got, want := cfg.serverMsize, uint32(32768); got != want {
		t.Fatalf("serverMsize = %d, want %d", got, want)
	}
	if got, want := cfg.clientMsize, uint32(32768); got != want {
		t.Fatalf("clientMsize = %d, want %d", got, want)
	}
}

// TestWithMsize_Overrides verifies later WithMsize calls overwrite
// earlier ones on both sides.
func TestWithMsize_Overrides(t *testing.T) {
	t.Parallel()
	cfg := newConfig()
	WithMsize(32768)(cfg)
	WithMsize(65536)(cfg)
	if got, want := cfg.serverMsize, uint32(65536); got != want {
		t.Fatalf("serverMsize = %d, want %d", got, want)
	}
	if got, want := cfg.clientMsize, uint32(65536); got != want {
		t.Fatalf("clientMsize = %d, want %d", got, want)
	}
}

// TestWithCtx_Sets verifies that WithCtx populates parentCtx.
func TestWithCtx_Sets(t *testing.T) {
	t.Parallel()
	cfg := newConfig()
	type key struct{}
	parent := context.WithValue(context.Background(), key{}, "marker")
	WithCtx(parent)(cfg)
	if cfg.parentCtx == nil {
		t.Fatal("parentCtx = nil, want non-nil")
	}
	if got, _ := cfg.parentCtx.Value(key{}).(string); got != "marker" {
		t.Fatalf("parentCtx Value = %q, want %q", got, "marker")
	}
}

// TestWithCtx_NilCoerced verifies that WithCtx(nil) is defensively
// coerced to context.Background() rather than panicking when the config
// is later consumed.
func TestWithCtx_NilCoerced(t *testing.T) {
	t.Parallel()
	cfg := newConfig()
	WithCtx(nil)(cfg)
	if cfg.parentCtx == nil {
		t.Fatal("parentCtx is nil after WithCtx(nil); want context.Background()")
	}
	// Any no-deadline ctx is acceptable — we just require non-nil so the
	// downstream context.WithTimeout derivation does not panic.
	if _, ok := cfg.parentCtx.Deadline(); ok {
		t.Fatal("parentCtx has a deadline; WithCtx(nil) should yield a background-equivalent ctx")
	}
}

// TestNewConfig_Defaults verifies newConfig yields working zero-state:
// empty option slices, zero msizes (signalling "use default"), a
// non-nil parentCtx.
func TestNewConfig_Defaults(t *testing.T) {
	t.Parallel()
	cfg := newConfig()
	if cfg == nil {
		t.Fatal("newConfig returned nil")
	}
	if len(cfg.serverOpts) != 0 {
		t.Errorf("serverOpts len = %d, want 0", len(cfg.serverOpts))
	}
	if len(cfg.clientOpts) != 0 {
		t.Errorf("clientOpts len = %d, want 0", len(cfg.clientOpts))
	}
	if cfg.serverMsize != 0 {
		t.Errorf("serverMsize = %d, want 0 (sentinel for default)", cfg.serverMsize)
	}
	if cfg.clientMsize != 0 {
		t.Errorf("clientMsize = %d, want 0 (sentinel for default)", cfg.clientMsize)
	}
	if cfg.parentCtx == nil {
		t.Error("parentCtx is nil; want context.Background()")
	}
}

// TestDefaultMsize_Reasonable pins defaultMsize at 65536 — mirrors the
// precedent set by client/pair_test.go. A change to this sentinel is a
// deliberate policy shift; this test forces the author to acknowledge it.
func TestDefaultMsize_Reasonable(t *testing.T) {
	t.Parallel()
	if defaultMsize != 65536 {
		t.Fatalf("defaultMsize = %d, want 65536", defaultMsize)
	}
}

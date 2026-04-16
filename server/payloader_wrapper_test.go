package server

import (
	"context"
	"testing"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// TestNonPayloaderRread_DoesNotSatisfyPayloader is the primary compile+runtime
// guard for the Phase 14 A/B bench harness: the wrapper MUST NOT satisfy
// proto.Payloader (otherwise sendResponseInline takes the writev+payload branch
// and the encode=copy arm measures the wrong path).
func TestNonPayloaderRread_DoesNotSatisfyPayloader(t *testing.T) {
	t.Parallel()

	var w any = &nonPayloaderRread{}
	if _, ok := w.(proto.Payloader); ok {
		t.Fatalf("*nonPayloaderRread unexpectedly satisfies proto.Payloader; "+
			"this breaks the encode=copy bench arm (sendResponseInline would take "+
			"the Payloader branch and measure the production path instead of the "+
			"EncodeTo fallback)")
	}
	if _, ok := w.(proto.Message); !ok {
		t.Fatalf("*nonPayloaderRread must satisfy proto.Message")
	}
	if _, ok := w.(releaser); !ok {
		t.Fatalf("*nonPayloaderRread must satisfy releaser to return bufpool slices")
	}
}

// TestForceCopyMiddleware_SwapsPooledRread exercises the middleware swap: when
// the wrapped handler returns a *pooledRread, the middleware must hand back a
// *nonPayloaderRread carrying the SAME bufPtr so the release contract is
// preserved.
func TestForceCopyMiddleware_SwapsPooledRread(t *testing.T) {
	t.Parallel()

	buf := make([]byte, 64)
	bufPtr := &buf
	inner := func(ctx context.Context, tag proto.Tag, msg proto.Message) proto.Message {
		return &pooledRread{Rread: proto.Rread{Data: buf[:4]}, bufPtr: bufPtr}
	}
	wrapped := forceCopyMiddleware(inner)
	resp := wrapped(context.Background(), proto.Tag(1), &proto.Tread{})
	swapped, ok := resp.(*nonPayloaderRread)
	if !ok {
		t.Fatalf("expected *nonPayloaderRread, got %T", resp)
	}
	if swapped.bufPtr != bufPtr {
		t.Fatalf("bufPtr ownership not forwarded: got %p, want %p", swapped.bufPtr, bufPtr)
	}
	if _, isPayloader := resp.(proto.Payloader); isPayloader {
		t.Fatalf("swapped response unexpectedly satisfies proto.Payloader")
	}
}

// TestForceCopyMiddleware_PassThroughNonRread asserts that responses other
// than *pooledRread are returned unchanged.
func TestForceCopyMiddleware_PassThroughNonRread(t *testing.T) {
	t.Parallel()

	orig := &p9l.Rlerror{Ecode: 42}
	inner := func(ctx context.Context, tag proto.Tag, msg proto.Message) proto.Message {
		return orig
	}
	wrapped := forceCopyMiddleware(inner)
	resp := wrapped(context.Background(), proto.Tag(1), &proto.Tread{})
	if resp != orig {
		t.Fatalf("pass-through failed: got %T, want *proto.Rlerror (identity)", resp)
	}
}

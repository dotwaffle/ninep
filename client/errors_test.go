package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"

	"github.com/dotwaffle/ninep/proto"
)

func TestError_Error_Rlerror(t *testing.T) {
	t.Parallel()
	e := &Error{Errno: proto.EACCES}
	got := e.Error()
	if !strings.Contains(got, "9p:") {
		t.Fatalf("Error() = %q, missing \"9p:\" prefix", got)
	}
	// Must carry the errno human name (from proto.Errno.Error()).
	if !strings.Contains(got, "permission denied") {
		t.Fatalf("Error() = %q, missing errno text", got)
	}
}

func TestError_Error_Rerror(t *testing.T) {
	t.Parallel()
	e := &Error{Errno: proto.ENOENT, Msg: "no such file"}
	got := e.Error()
	if !strings.Contains(got, "no such file") {
		t.Fatalf("Error() = %q, missing Msg", got)
	}
	if !strings.Contains(got, "9p:") {
		t.Fatalf("Error() = %q, missing \"9p:\" prefix", got)
	}
}

func TestError_Is_ProtoErrno(t *testing.T) {
	t.Parallel()
	e := &Error{Errno: proto.EACCES}
	if !errors.Is(e, proto.EACCES) {
		t.Fatalf("errors.Is(&Error{EACCES}, proto.EACCES) = false, want true")
	}
}

func TestError_Is_DifferentErrno(t *testing.T) {
	t.Parallel()
	e := &Error{Errno: proto.EACCES}
	if errors.Is(e, proto.ENOENT) {
		t.Fatalf("errors.Is(&Error{EACCES}, proto.ENOENT) = true, want false")
	}
}

func TestError_Is_Sentinel(t *testing.T) {
	t.Parallel()
	if !errors.Is(ErrClosed, ErrClosed) {
		t.Fatalf("errors.Is(ErrClosed, ErrClosed) = false")
	}
	if !errors.Is(ErrNotSupported, ErrNotSupported) {
		t.Fatalf("errors.Is(ErrNotSupported, ErrNotSupported) = false")
	}
	if !errors.Is(ErrVersionMismatch, ErrVersionMismatch) {
		t.Fatalf("errors.Is(ErrVersionMismatch, ErrVersionMismatch) = false")
	}
	if !errors.Is(ErrMsizeTooSmall, ErrMsizeTooSmall) {
		t.Fatalf("errors.Is(ErrMsizeTooSmall, ErrMsizeTooSmall) = false")
	}
}

func TestError_Is_WrappedClosed(t *testing.T) {
	t.Parallel()
	wrapped := fmt.Errorf("op: %w", ErrClosed)
	if !errors.Is(wrapped, ErrClosed) {
		t.Fatalf("errors.Is(wrapped, ErrClosed) = false")
	}
}

func TestError_Is_NotSupportedDistinct(t *testing.T) {
	t.Parallel()
	if errors.Is(ErrNotSupported, ErrClosed) {
		t.Fatalf("sentinels collapsed: ErrNotSupported == ErrClosed")
	}
	if errors.Is(ErrClosed, ErrVersionMismatch) {
		t.Fatalf("sentinels collapsed: ErrClosed == ErrVersionMismatch")
	}
}

// TestError_Is_SyscallBridge asserts the status of Assumption A1 from
// 19-RESEARCH.md: does errors.Is(proto.Errno, syscall.Errno) bridge the
// two types on Linux?
//
// Empirical probe (performed during Task 3 execution):
//
//	errors.Is(proto.ENOENT, syscall.ENOENT) == false
//
// Even though both values are numerically 2, proto.Errno.Is only matches
// against other proto.Errno values. The bridge is NOT available — callers
// must use proto.Errno constants with errors.Is, not syscall.Errno.
//
// This test pins that behavior so any future change (e.g. adding a
// syscall.Errno branch to proto.Errno.Is) must update both sides together.
func TestError_Is_SyscallBridge(t *testing.T) {
	t.Parallel()

	// Sanity: the raw proto.Errno has no syscall bridge.
	if errors.Is(proto.ENOENT, syscall.ENOENT) {
		t.Fatalf("A1 changed: errors.Is(proto.ENOENT, syscall.ENOENT) is now true; update godoc")
	}

	// *Error delegates to proto.Errno.Is, so the same non-bridge applies.
	e := &Error{Errno: proto.ENOENT}
	if errors.Is(e, syscall.ENOENT) {
		t.Fatalf("A1 changed: errors.Is(&Error{ENOENT}, syscall.ENOENT) is now true; update godoc")
	}

	// But proto.Errno form works — this is what callers must use.
	if !errors.Is(e, proto.ENOENT) {
		t.Fatalf("errors.Is(&Error{ENOENT}, proto.ENOENT) = false")
	}
}

// TestError_SentinelsAreFourDistinctValues exists to catch accidental
// sharing of errors.New values across sentinels (e.g. two identical strings).
func TestError_SentinelsAreFourDistinctValues(t *testing.T) {
	t.Parallel()
	sentinels := []error{ErrClosed, ErrNotSupported, ErrVersionMismatch, ErrMsizeTooSmall}
	for i := range sentinels {
		for j := range sentinels {
			if i == j {
				continue
			}
			if errors.Is(sentinels[i], sentinels[j]) {
				t.Fatalf("sentinel %d and %d collapse under errors.Is", i, j)
			}
		}
	}
}

// TestErrorChain_FlushAndCtx_Canceled verifies RESEARCH Assumption A1: a
// fmt.Errorf("...: %w", errors.Join(a, b)) chain satisfies errors.Is for BOTH
// joined children. Plan 22-02's flushAndWait depends on this semantic to
// compose the Rflush-first error chain per D-05/D-08.
func TestErrorChain_FlushAndCtx_Canceled(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("9p: flushed tag 1: %w",
		errors.Join(context.Canceled, ErrFlushed))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("errors.Is(err, context.Canceled) = false, want true (A1 broken)")
	}
	if !errors.Is(err, ErrFlushed) {
		t.Fatalf("errors.Is(err, ErrFlushed) = false, want true (A1 broken)")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(err, context.DeadlineExceeded) = true, want false")
	}
}

// TestErrorChain_FlushAndCtx_DeadlineExceeded is the deadline variant of the
// composite-chain check — same semantics, with context.DeadlineExceeded in
// place of context.Canceled.
func TestErrorChain_FlushAndCtx_DeadlineExceeded(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("9p: flushed tag 1: %w",
		errors.Join(context.DeadlineExceeded, ErrFlushed))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(err, context.DeadlineExceeded) = false, want true (A1 broken)")
	}
	if !errors.Is(err, ErrFlushed) {
		t.Fatalf("errors.Is(err, ErrFlushed) = false, want true (A1 broken)")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("errors.Is(err, context.Canceled) = true, want false")
	}
}

// TestErrorChain_FlushOnly_NoCtx covers the R-first path analog per D-05:
// if the original R arrives before Rflush, ErrFlushed is NOT in the chain —
// only ctx.Err() is wrapped. Callers discriminate on ErrFlushed to detect
// "server acked my Tflush" vs "I cancelled but the response beat the flush".
func TestErrorChain_FlushOnly_NoCtx(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("9p: flushed tag 1: %w", context.Canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("errors.Is(err, context.Canceled) = false, want true")
	}
	if errors.Is(err, ErrFlushed) {
		t.Fatalf("errors.Is(err, ErrFlushed) = true, want false (R-first path must not match ErrFlushed)")
	}
}

// TestErrFlushed_Distinct_From_ErrClosed pins D-11: ErrFlushed and ErrClosed
// are operationally meaningful as distinct states ("server acked my flush"
// vs "connection is gone"). Callers must be able to discriminate.
func TestErrFlushed_Distinct_From_ErrClosed(t *testing.T) {
	t.Parallel()
	if errors.Is(ErrFlushed, ErrClosed) {
		t.Fatalf("errors.Is(ErrFlushed, ErrClosed) = true, want false (sentinels collapsed)")
	}
	if errors.Is(ErrClosed, ErrFlushed) {
		t.Fatalf("errors.Is(ErrClosed, ErrFlushed) = true, want false (sentinels collapsed)")
	}
	if ErrFlushed.Error() == ErrClosed.Error() {
		t.Fatalf("ErrFlushed and ErrClosed share text %q", ErrFlushed.Error())
	}
}

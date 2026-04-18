package client

import (
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

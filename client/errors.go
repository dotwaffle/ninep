package client

import (
	"errors"
	"fmt"

	"github.com/dotwaffle/ninep/proto"
)

// Sentinel errors exposed by the client package. Callers match these with
// [errors.Is] for lifecycle-level error discrimination. Per-operation 9P
// errors (EACCES, ENOENT, etc.) are surfaced as a *[Error] wrapping a
// [proto.Errno] — use [errors.Is] with the proto.Errno constant for those.
var (
	// ErrClosed is returned when a request is made against, or blocked on,
	// a Conn that has been Closed or whose underlying net.Conn returned an
	// I/O error that caused shutdown.
	ErrClosed = errors.New("client: connection closed")

	// ErrNotSupported is returned when a method is called on a Conn whose
	// negotiated dialect does not support the operation — e.g. a
	// 9P2000.L-only method invoked on a .u-negotiated Conn (see package
	// doc for the full .L-only list). This sentinel is distinct from
	// [proto.ENOTSUP] (errno 95); dialect-guard checks compare against
	// ErrNotSupported, server-returned errno responses compare against
	// proto.ENOTSUP.
	ErrNotSupported = errors.New("client: operation not supported by negotiated dialect")

	// ErrVersionMismatch is returned by the Conn constructor when the
	// server's Rversion carries a version string that is neither
	// "9P2000.L" nor "9P2000.u".
	ErrVersionMismatch = errors.New("client: version mismatch")

	// ErrMsizeTooSmall is returned by the Conn constructor when the
	// negotiated msize (min of client proposal and server cap) is below
	// the minimum required to carry a useful payload.
	ErrMsizeTooSmall = errors.New("client: msize too small")

	// ErrFidExhausted is returned when the per-Conn fid counter has run
	// past proto.NoFid (2^32 - 2 allocations). Unreachable under any
	// practical workload; documented for completeness. Callers that
	// encounter it should Dial a new Conn.
	ErrFidExhausted = errors.New("client: fid space exhausted")

	// ErrDialectInvariant signals that Conn.dialect holds a value outside
	// the {protocolL, protocolU} set expected after Dial. Dialect is set
	// once at negotiation time and never mutated afterwards, so reaching
	// this sentinel means either (1) a future refactor forgot to update a
	// dialect switch, or (2) memory corruption. It is wrapped into the
	// default arm of dialect switches in session helpers so callers can
	// errors.Is against a stable sentinel rather than string-matching the
	// wrapping fmt.Errorf.
	ErrDialectInvariant = errors.New("client: dialect invariant violated (programmer error)")
)

// Error represents a 9P error response from the server. Rlerror (9P2000.L)
// populates only Errno; Rerror (9P2000.u) populates both Errno and Msg (the
// .u extension's human-readable ename). Callers treat the two dialects
// uniformly via this single type.
//
// The Msg field is server-controlled on .u connections — callers that log
// errors from untrusted servers should redact or bound-check Msg before
// emitting it at info-level.
type Error struct {
	// Errno is the 9P errno value carried on the wire. Always populated.
	Errno proto.Errno

	// Msg is the server-supplied human-readable string from Rerror.Ename
	// on a .u-negotiated Conn. Empty for Rlerror responses.
	Msg string
}

// Error formats the error. When Msg is set (Rerror on .u), the output is
// "9p: <errno>: <msg>"; otherwise it is "9p: <errno>".
func (e *Error) Error() string {
	if e.Msg != "" {
		return fmt.Sprintf("9p: %s: %s", e.Errno.Error(), e.Msg)
	}
	return "9p: " + e.Errno.Error()
}

// Is delegates to [proto.Errno.Is], so the idiomatic Go pattern works:
//
//	if errors.Is(err, proto.EACCES) { ... }
//
// Note: proto.Errno.Is matches only against other proto.Errno targets. It
// does NOT bridge to syscall.Errno — even though the numeric values match
// on Linux, errors.Is(&client.Error{Errno: proto.ENOENT}, syscall.ENOENT)
// returns false. Use proto.Errno constants for portable error discrimination.
// This is Assumption A1 from .planning/phases/19/19-RESEARCH.md §9, verified
// false during Plan 19-01 execution.
func (e *Error) Is(target error) bool {
	return errors.Is(e.Errno, target)
}

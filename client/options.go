package client

import (
	"log/slog"
	"time"

	"github.com/dotwaffle/ninep/proto"
)

// Option configures a Conn. Options are applied by the Conn constructor in the
// order they are supplied.
type Option func(*config)

// config holds the resolved Conn configuration. It is unexported: callers
// mutate it only through Option values.
type config struct {
	msize            uint32
	version          proto.Version
	maxInflight      int
	logger           *slog.Logger
	lockPollSchedule []time.Duration
	// requestTimeout is the default ctx timeout applied by File.Read,
	// File.Write, File.ReadAt, File.WriteAt (the non-ctx io.* methods).
	// Zero (the default) means infinite wait — matches the Linux v9fs
	// kernel client for trans=tcp mounts (Pitfall 9 / D-22). Values
	// < 0 are coerced to 0 by WithRequestTimeout. Mutates behaviour
	// only of the non-ctx io.* methods; the *Ctx variants honor the
	// caller-supplied ctx verbatim.
	requestTimeout time.Duration
}

// Defaults for Conn configuration.
const (
	// defaultMsize is the proposed maximum message size. 1 MiB matches the
	// Linux kernel v9fs client default for trans=tcp mounts (D-14).
	defaultMsize uint32 = 1 << 20

	// defaultMaxInflight is the number of concurrent outstanding requests
	// per Conn. Mirrors server.WithMaxInflight's default.
	defaultMaxInflight int = 64

	// maxMaxInflight is the upper bound on maxInflight. NoTag (0xFFFF) is
	// reserved for Tversion and is excluded from the free-list, so the
	// allocator can hold at most uint16(proto.NoTag)-1 = 65534 tags. A
	// package-level compile-time check below pins this to proto.NoTag so
	// any change to the proto constant surfaces here immediately.
	maxMaxInflight int = 65534
)

// Compile-time assertion: maxMaxInflight must equal uint16(proto.NoTag)-1.
// If proto.NoTag ever changes from math.MaxUint16, this array's size goes
// negative and the package fails to build.
var _ = [1]struct{}{}[int(uint16(proto.NoTag))-1-maxMaxInflight]

// newConfig returns a config populated with defaults. Options applied on top
// of the returned config mutate it in place.
func newConfig() *config {
	return &config{
		msize:       defaultMsize,
		maxInflight: defaultMaxInflight,
		logger:      slog.Default(),
	}
}

// WithVersion sets the protocol version to negotiate during Dial. When set,
// the client proposes this version and returns an error if the server
// negotiates any other version (including lower versions). This is useful
// for deterministic testing of protocol-specific logic.
//
// When not set, the client proposes the highest supported version
// ([proto.VersionL]) and accepts whatever the server negotiates.
func WithVersion(v proto.Version) Option {
	return func(c *config) { c.version = v }
}

// WithMsize sets the proposed maximum message size. The default is 1 MiB
// (1 << 20), chosen to match the Linux kernel v9fs client for interop parity
// (see package documentation). The server's Rversion msize caps the proposal;
// the negotiated msize is min(client proposal, server cap).
//
// No clamping is performed on the input — callers that proposed 0 or a value
// smaller than [proto.HeaderSize] will surface [ErrMsizeTooSmall] at Dial
// time, not here.
func WithMsize(n uint32) Option {
	return func(c *config) { c.msize = n }
}

// WithMaxInflight sets the maximum number of concurrent outstanding requests
// on the Conn. The free-list tag allocator uses this as its channel capacity,
// so back-pressure kicks in at this value — once saturated, new requests
// block until an in-flight tag is released.
//
// Values less than 1 are clamped to 1. Values greater than 65534 are clamped
// to 65534: NoTag (0xFFFF) is reserved for Tversion and is excluded from the
// free-list. Default: 64.
func WithMaxInflight(n int) Option {
	return func(c *config) {
		if n < 1 {
			n = 1
		}
		if n > maxMaxInflight {
			n = maxMaxInflight
		}
		c.maxInflight = n
	}
}

// WithLogger sets the structured logger used by the Conn for diagnostic
// output. A nil logger is ignored — the existing logger (by default
// [slog.Default]) is preserved.
func WithLogger(logger *slog.Logger) Option {
	return func(c *config) {
		if logger != nil {
			c.logger = logger
		}
	}
}

// WithLockPollSchedule overrides the default exponential backoff curve
// used by [File.Lock] when the server returns LockStatusBlocked or
// LockStatusGrace. Values are the sleep durations for iterations 0..N;
// iterations past N use the last entry as a cap.
//
// Passing an empty slice is a programming error and silently falls back
// to the default schedule ([DefaultLockBackoff]).
//
// Primarily used by tests to bound timing with a sub-millisecond cadence
// (deterministic timing for contention tests without a minute-long wall
// clock). Production callers should leave the default
// (10/20/40/80/160/320/500ms cap) in place.
func WithLockPollSchedule(schedule []time.Duration) Option {
	return func(c *config) {
		if len(schedule) == 0 {
			return
		}
		// Defensive copy: callers mutating their slice after Dial
		// should not affect the resolved Conn config.
		c.lockPollSchedule = append([]time.Duration(nil), schedule...)
	}
}

// WithRequestTimeout sets a per-request timeout applied to the non-ctx
// [File.Read], [File.Write], [File.ReadAt], and [File.WriteAt] methods.
// When set to a positive duration d, each call builds a context via
// [context.WithTimeout] with that duration; timeout expiry triggers
// Tflush via the standard roundTrip cancellation pipeline (Plan 22-02)
// and returns an error chain where [errors.Is] matches
// [context.DeadlineExceeded].
//
// The default (zero) means infinite wait — matches the Linux kernel
// v9fs client for trans=tcp mounts (Pitfall 9 / D-22). Callers that need
// per-op deadlines without a Conn-wide default use [File.ReadCtx],
// [File.WriteCtx], [File.ReadAtCtx], [File.WriteAtCtx] with a
// caller-supplied ctx instead of this option.
//
// Values <= 0 (zero or negative) are treated as "no timeout"; this keeps
// the surface Linux-v9fs-parallel and prevents accidental pathological
// short timeouts from callers passing a time.Duration literal with a
// zero-value or a subtraction overflow.
//
// Per-op precedence: if a caller passes a ctx WITH a deadline to a *Ctx
// variant (e.g. [File.ReadCtx]), that ctx is used verbatim —
// WithRequestTimeout is ignored on the *Ctx methods.
func WithRequestTimeout(d time.Duration) Option {
	return func(c *config) {
		if d < 0 {
			d = 0
		}
		c.requestTimeout = d
	}
}

package client

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dotwaffle/ninep/proto"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Option configures a Conn. Options are applied by the Conn constructor in the
// order they are supplied.
type Option func(*config)

// config holds the resolved Conn configuration. It is unexported: callers
// mutate it only through Option values.
type config struct {
	err              error
	msize            uint32
	version          proto.Version
	maxInflight      int
	logger           *slog.Logger
	lockPollSchedule []time.Duration
	// requestTimeout is the default ctx timeout applied by File.Read,
	// File.Write, File.ReadAt, File.WriteAt (the non-ctx io.* methods).
	// Zero (the default) means infinite wait - matches the Linux v9fs
	// kernel client for trans=tcp mounts. Negative values are invalid. The
	// *Ctx variants honor the caller-supplied ctx verbatim.
	requestTimeout time.Duration
	cleanupTimeout time.Duration
	tracerProvider trace.TracerProvider
	meterProvider  metric.MeterProvider
}

// Defaults for Conn configuration.
const (
	// defaultMsize is the proposed maximum message size. 1 MiB matches the
	// Linux kernel v9fs client default for trans=tcp mounts.
	defaultMsize uint32 = 1 << 20

	// defaultMaxInflight is the number of concurrent outstanding requests
	// per Conn. Mirrors server.WithMaxInflight's default.
	defaultMaxInflight int = 64

	defaultCleanupTimeout = 5 * time.Second

	// maxMaxInflight is the upper bound on maxInflight. The tag space is
	// split in half: request tags live in [1..maxMaxInflight] and each
	// request's Tflush uses the reserved mirror tag oldTag|flushTagBit
	// (see flushTagFor), so flush tags occupy [flushTagBit+1 ..
	// flushTagBit+maxMaxInflight]. The top mirror slot is left unused so
	// no flush tag can collide with NoTag (0xFFFF, reserved for
	// Tversion): flushTagBit + maxMaxInflight = 65534 < NoTag. A
	// package-level compile-time check below pins this relation so any
	// change to the proto constant or the bit surfaces immediately.
	maxMaxInflight int = 32766
)

// Compile-time assertion: the highest flush tag (flushTagBit +
// maxMaxInflight) must stay below NoTag. If proto.NoTag ever changes from
// math.MaxUint16, this array's size goes negative and the package fails
// to build.
var _ = [1]struct{}{}[int(uint16(proto.NoTag))-1-(flushTagBit+maxMaxInflight)]

// newConfig returns a config populated with defaults. Options applied on top
// of the returned config mutate it in place.
func newConfig() *config {
	return &config{
		msize:          defaultMsize,
		maxInflight:    defaultMaxInflight,
		cleanupTimeout: defaultCleanupTimeout,
		logger:         slog.Default(),
	}
}

// WithCleanupTimeout bounds internal Tclunk operations used to retire fids
// after Close and failure-path cleanup. Cleanup does not use the ordinary
// request cancellation flush grace: an unacknowledged fid is quarantined
// instead of delaying the caller or risking reuse. Dial rejects non-positive
// values. Default: 5s.
func WithCleanupTimeout(d time.Duration) Option {
	return func(c *config) {
		c.cleanupTimeout = d
		if d <= 0 {
			c.setError(errors.New("client: cleanup timeout must be positive"))
		}
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
// Dial rejects values above [proto.MaxMessageSize] before touching the
// connection. Values below the negotiated minimum surface [ErrMsizeTooSmall].
func WithMsize(n uint32) Option {
	return func(c *config) {
		c.msize = n
		switch {
		case n < minMsize:
			c.setError(ErrMsizeTooSmall)
		case n > proto.MaxMessageSize:
			c.setError(ErrMsizeTooLarge)
		}
	}
}

// WithMaxInflight sets the maximum number of concurrent outstanding requests
// on the Conn. The free-list tag allocator uses this as its channel capacity,
// so back-pressure kicks in at this value -- once saturated, new requests
// block until an in-flight tag is released.
//
// Dial rejects values outside 1..32766. The upper half of the tag space is
// reserved for the Tflush mirror tags that cancellation sends (see
// flushTagFor), and NoTag (0xFFFF) is reserved for Tversion. Default: 64.
func WithMaxInflight(n int) Option {
	return func(c *config) {
		c.maxInflight = n
		if n < 1 || n > maxMaxInflight {
			c.setError(fmt.Errorf("client: max inflight %d outside [1, %d]", n, maxMaxInflight))
		}
	}
}

// WithLogger sets the structured logger used by the Conn for diagnostic
// output. Dial rejects a nil logger.
func WithLogger(logger *slog.Logger) Option {
	return func(c *config) {
		c.logger = logger
		if logger == nil {
			c.setError(errors.New("client: logger must not be nil"))
		}
	}
}

// WithLockPollSchedule overrides the default exponential backoff curve
// used by [File.Lock] when the server returns LockStatusBlocked or
// LockStatusGrace. Values are the sleep durations for iterations 0..N;
// iterations past N use the last entry as a cap.
//
// Dial rejects an empty slice or negative duration.
//
// Primarily used by tests to bound timing with a sub-millisecond cadence
// (deterministic timing for contention tests without a minute-long wall
// clock). Production callers should leave the default
// (10/20/40/80/160/320/500ms cap) in place.
func WithLockPollSchedule(schedule []time.Duration) Option {
	return func(c *config) {
		if len(schedule) == 0 {
			c.setError(errors.New("client: lock poll schedule must not be empty"))
			return
		}
		for _, delay := range schedule {
			if delay < 0 {
				c.setError(errors.New("client: lock poll schedule must not contain negative durations"))
				return
			}
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
// Tflush via the standard roundTrip cancellation pipeline and returns
// an error chain where [errors.Is] matches [context.DeadlineExceeded].
//
// The default (zero) means infinite wait - matches the Linux kernel
// v9fs client for trans=tcp mounts. Callers that need
// per-op deadlines without a Conn-wide default use [File.ReadCtx],
// [File.WriteCtx], [File.ReadAtCtx], [File.WriteAtCtx] with a
// caller-supplied ctx instead of this option.
//
// Zero disables the timeout. Dial rejects negative values.
//
// Per-op precedence: if a caller passes a ctx WITH a deadline to a *Ctx
// variant (e.g. [File.ReadCtx]), that ctx is used verbatim -
// WithRequestTimeout is ignored on the *Ctx methods.
func WithRequestTimeout(d time.Duration) Option {
	return func(c *config) {
		c.requestTimeout = d
		if d < 0 {
			c.setError(errors.New("client: request timeout must not be negative"))
		}
	}
}

// WithTracer sets the TracerProvider used by the Conn for instrumentation.
// Dial rejects nil; omit the option to disable tracing.
func WithTracer(tp trace.TracerProvider) Option {
	return func(c *config) {
		c.tracerProvider = tp
		if tp == nil {
			c.setError(errors.New("client: tracer provider must not be nil"))
		}
	}
}

// WithMeter sets the MeterProvider used by the Conn for instrumentation.
// Dial rejects nil; omit the option to disable metrics.
func WithMeter(mp metric.MeterProvider) Option {
	return func(c *config) {
		c.meterProvider = mp
		if mp == nil {
			c.setError(errors.New("client: meter provider must not be nil"))
		}
	}
}

func (c *config) setError(err error) {
	if c.err == nil {
		c.err = err
	}
}

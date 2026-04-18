package client

import (
	"log/slog"

	"github.com/dotwaffle/ninep/proto"
)

// Option configures a Conn. Options are applied by the Conn constructor in the
// order they are supplied.
type Option func(*config)

// config holds the resolved Conn configuration. It is unexported: callers
// mutate it only through Option values.
type config struct {
	msize       uint32
	maxInflight int
	logger      *slog.Logger
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

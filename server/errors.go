package server

import "errors"

// Sentinel errors for the server package.
var (
	// ErrFidInUse is returned when attempting to allocate a fid that is
	// already present in the fid table.
	ErrFidInUse = errors.New("fid already in use")

	// ErrNotNegotiated is returned when a message arrives before version
	// negotiation has completed.
	ErrNotNegotiated = errors.New("version not negotiated")

	// ErrMsizeTooSmall is returned when the client proposes an msize that
	// is too small to carry any useful payload.
	ErrMsizeTooSmall = errors.New("msize too small")

	// ErrFidLimitExceeded is returned by fidTable.add when the configured
	// per-connection fid cap (see WithMaxFids) has been reached. Handlers
	// map this to proto.EMFILE in the response.
	ErrFidLimitExceeded = errors.New("fid limit exceeded")
)

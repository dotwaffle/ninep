package server

import "errors"

// Sentinel errors for the server package.
var (
	// ErrFidInUse is returned when attempting to allocate a fid that is
	// already present in the fid table.
	ErrFidInUse = errors.New("fid already in use")

	// ErrFidNotFound is returned when a fid lookup fails.
	ErrFidNotFound = errors.New("fid not found")

	// ErrNotNegotiated is returned when a message arrives before version
	// negotiation has completed.
	ErrNotNegotiated = errors.New("version not negotiated")

	// ErrMsizeTooSmall is returned when the client proposes an msize that
	// is too small to carry any useful payload.
	ErrMsizeTooSmall = errors.New("msize too small")

	// ErrNotDirectory is returned when a walk targets a node that does not
	// implement NodeLookuper.
	ErrNotDirectory = errors.New("not a directory")
)

package client

import "errors"

// ErrClosed is returned when a request is made against, or blocked on, a
// Conn that has been Closed or whose underlying net.Conn returned an I/O
// error that caused shutdown.
//
// NOTE: this is a minimal stub. Task 3 of Plan 19-01 expands this file with
// the full sentinel set and the Error type.
var ErrClosed = errors.New("client: connection closed")

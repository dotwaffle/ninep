//go:build !linux && !freebsd

package proto

import "syscall"

// ErrnoFromUnix is the identity translation on platforms without a defined
// errno divergence from Linux. Provided so the API surface exists everywhere
// (including platforms where golang.org/x/sys/unix is unavailable, e.g.
// windows) and consumers don't need build tags around their callsites.
func ErrnoFromUnix(e syscall.Errno) Errno {
	return Errno(e)
}

//go:build linux

package proto

import "golang.org/x/sys/unix"

// ErrnoFromUnix converts a unix.Errno to a proto.Errno wire value.
// On Linux this is identity: errno numbers in the syscall package match
// the 9P2000.L wire format verbatim.
func ErrnoFromUnix(e unix.Errno) Errno {
	return Errno(e)
}

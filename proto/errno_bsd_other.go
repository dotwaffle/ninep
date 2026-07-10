//go:build openbsd || netbsd

package proto

import "golang.org/x/sys/unix"

// ErrnoFromUnix converts an OpenBSD/NetBSD unix.Errno to its Linux wire
// equivalent. These platforms share FreeBSD's BSD errno layout for the
// POSIX-stable range (1..34) and reuse errno 11 for EDEADLK where Linux
// uses it for EAGAIN, exactly as FreeBSD does -- see errno_freebsd.go's
// freebsdToLinuxErrno for the reasoning. Their extended (>34) errno tables
// diverge from both Linux and FreeBSD's numbering in ways not verified
// here, so rather than silently pass through a wrong wire value, extended
// errnos map to EIO. Only the POSIX-stable range and the EDEADLK/EAGAIN
// trap are translated precisely.
func ErrnoFromUnix(e unix.Errno) Errno {
	switch e {
	case 0:
		return 0
	case unix.EDEADLK:
		return EDEADLK
	case unix.EWOULDBLOCK: // == EAGAIN on these platforms.
		return EAGAIN
	}
	if e <= 34 {
		return Errno(e)
	}
	return EIO
}

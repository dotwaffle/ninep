//go:build !linux && !freebsd && !darwin && !openbsd && !netbsd

package proto

import "syscall"

// ErrnoFromUnix is the identity translation for platforms with no defined
// 9P errno mapping (windows, solaris, illumos, plan9, js/wasm, aix, and
// any future GOOS not covered by a dedicated errno_*.go file). These
// platforms are not verified against the Linux errno layout 9P2000.L
// requires on the wire, so this is a best-effort default rather than a
// correctness guarantee -- it exists so the API surface compiles
// everywhere and consumers don't need build tags around their callsites.
// The parameter is syscall.Errno rather than golang.org/x/sys/unix.Errno
// because the unix package does not build on windows; on every platform
// where both packages are available, unix.Errno is a type alias for
// syscall.Errno (see golang.org/x/sys/unix/aliases.go), so callers on
// those platforms can pass either interchangeably.
func ErrnoFromUnix(e syscall.Errno) Errno {
	return Errno(e)
}

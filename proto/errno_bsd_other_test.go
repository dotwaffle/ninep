//go:build openbsd || netbsd

package proto

import (
	"testing"

	"golang.org/x/sys/unix"
)

// TestErrnoFromUnixDivergence covers the deliberately narrow OpenBSD/NetBSD
// mapping: the POSIX-stable range passes through, the EDEADLK/EAGAIN slot
// trap is translated, and everything above 34 degrades to EIO.
func TestErrnoFromUnixDivergence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   unix.Errno
		want Errno
	}{
		// The shared BSD slot-11 trap.
		{"EAGAIN", unix.EAGAIN, EAGAIN},
		{"EWOULDBLOCK", unix.EWOULDBLOCK, EAGAIN},
		{"EDEADLK", unix.EDEADLK, EDEADLK},
		// Extended errnos are not verified against Linux numbering on these
		// platforms and must degrade to EIO rather than pass through wrong.
		{"ENOTSUP", unix.ENOTSUP, EIO},
		{"ETIMEDOUT", unix.ETIMEDOUT, EIO},
		{"ESTALE", unix.ESTALE, EIO},
		// POSIX-stable: pass-through (1..34).
		{"EPERM", unix.EPERM, EPERM},
		{"ENOENT", unix.ENOENT, ENOENT},
		{"EIO", unix.EIO, EIO},
		{"EBADF", unix.EBADF, EBADF},
		{"EACCES", unix.EACCES, EACCES},
		{"EEXIST", unix.EEXIST, EEXIST},
		{"ENOTDIR", unix.ENOTDIR, ENOTDIR},
		{"EISDIR", unix.EISDIR, EISDIR},
		{"EINVAL", unix.EINVAL, EINVAL},
		{"ERANGE", unix.ERANGE, ERANGE},
		// Zero is preserved.
		{"zero", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ErrnoFromUnix(tc.in); got != tc.want {
				t.Fatalf("ErrnoFromUnix(%s=%d) = %d, want %d", tc.name, tc.in, got, tc.want)
			}
		})
	}
}

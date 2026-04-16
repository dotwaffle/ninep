//go:build freebsd

package proto

import (
	"testing"

	"golang.org/x/sys/unix"
)

// TestErrnoFromUnixDivergence covers the errnos most likely to be observed
// in 9P operations, ensuring each translates to the correct Linux wire value.
func TestErrnoFromUnixDivergence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   unix.Errno
		want Errno
	}{
		// Divergent errnos (FreeBSD value -> Linux wire value).
		{"EAGAIN", unix.EAGAIN, EAGAIN},
		{"EWOULDBLOCK", unix.EWOULDBLOCK, EAGAIN},
		{"EDEADLK", unix.EDEADLK, EDEADLK}, // POSIX-stable: 11 on both.
		{"ENAMETOOLONG", unix.ENAMETOOLONG, ENAMETOOLONG},
		{"ENOLCK", unix.ENOLCK, ENOLCK},
		{"ENOSYS", unix.ENOSYS, ENOSYS},
		{"ENOTEMPTY", unix.ENOTEMPTY, ENOTEMPTY},
		{"ELOOP", unix.ELOOP, ELOOP},
		{"EOPNOTSUPP", unix.EOPNOTSUPP, ENOTSUP},
		{"ENOTSUP", unix.ENOTSUP, ENOTSUP},
		{"ESTALE", unix.ESTALE, ESTALE},
		{"ETIMEDOUT", unix.ETIMEDOUT, ETIMEDOUT},
		{"EDQUOT", unix.EDQUOT, EDQUOT},
		{"ECANCELED", unix.ECANCELED, ECANCELED},
		{"EOVERFLOW", unix.EOVERFLOW, EOVERFLOW},
		{"EREMOTE", unix.EREMOTE, EREMOTE},
		{"EPROTO", unix.EPROTO, EPROTO},
		{"EMSGSIZE", unix.EMSGSIZE, EMSGSIZE},
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

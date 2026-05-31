//go:build darwin

package proto

import (
	"testing"

	"golang.org/x/sys/unix"
)

// TestErrnoFromUnixDivergence covers the errnos most likely to be observed
// in 9P operations on a Darwin-hosted server, ensuring each translates to
// the correct Linux wire value.
func TestErrnoFromUnixDivergence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   unix.Errno
		want Errno
	}{
		// Divergent errnos (Darwin value -> Linux wire value).
		{"EAGAIN", unix.EAGAIN, EAGAIN},
		{"EWOULDBLOCK", unix.EWOULDBLOCK, EAGAIN},
		{"EDEADLK", unix.EDEADLK, EDEADLK}, // Darwin slot 11 -> Linux 35; only the name is shared, not the value.
		{"ENAMETOOLONG", unix.ENAMETOOLONG, ENAMETOOLONG},
		{"ENOLCK", unix.ENOLCK, ENOLCK},
		{"ENOSYS", unix.ENOSYS, ENOSYS},
		{"ENOTEMPTY", unix.ENOTEMPTY, ENOTEMPTY},
		{"ELOOP", unix.ELOOP, ELOOP},
		{"ENOTSUP", unix.ENOTSUP, ENOTSUP},
		// EOPNOTSUPP is a distinct number (102) from ENOTSUP (45) on Darwin,
		// unlike FreeBSD; it must still translate to the Linux ENOTSUP value
		// (95), not pass through to the colliding ENETRESET (102).
		{"EOPNOTSUPP", unix.EOPNOTSUPP, ENOTSUP},
		{"ESTALE", unix.ESTALE, ESTALE},
		{"ETIMEDOUT", unix.ETIMEDOUT, ETIMEDOUT},
		{"EDQUOT", unix.EDQUOT, EDQUOT},
		{"ECANCELED", unix.ECANCELED, ECANCELED},
		{"EIDRM", unix.EIDRM, EIDRM},
		{"ENOMSG", unix.ENOMSG, ENOMSG},
		{"EILSEQ", unix.EILSEQ, EILSEQ},
		{"EBADMSG", unix.EBADMSG, EBADMSG},
		{"EMULTIHOP", unix.EMULTIHOP, EMULTIHOP},
		{"ENOLINK", unix.ENOLINK, ENOLINK},
		{"EOVERFLOW", unix.EOVERFLOW, EOVERFLOW},
		{"EREMOTE", unix.EREMOTE, EREMOTE},
		{"EPROTO", unix.EPROTO, EPROTO},
		{"EMSGSIZE", unix.EMSGSIZE, EMSGSIZE},
		{"ENOATTR", unix.ENOATTR, ENODATA}, // xattr "not found" -> Linux ENODATA.
		// Darwin-only errno with no Linux equivalent falls through to EIO.
		{"EFTYPE", unix.EFTYPE, EIO},
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

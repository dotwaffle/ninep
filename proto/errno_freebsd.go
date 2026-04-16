//go:build freebsd

package proto

import "golang.org/x/sys/unix"

// ErrnoFromUnix converts a FreeBSD unix.Errno to its Linux wire equivalent.
// 9P2000.L defines errno numbers as the Linux UAPI values; without translation
// a FreeBSD server would send wrong wire values (e.g. EAGAIN=35 instead of 11).
//
// Errnos 1..34 are POSIX-stable and shared between Linux and FreeBSD; they
// pass through unchanged. The freebsdToLinuxErrno table covers errnos that
// diverge between the two platforms. Unmapped FreeBSD-only errnos (EPROCLIM,
// EBADRPC, ERPCMISMATCH, EPROGUNAVAIL, EPROGMISMATCH, EPROCUNAVAIL, EFTYPE,
// EAUTH, ENEEDAUTH, ENOATTR, EDOOFUS) fall through to EIO.
//
// Verified against
// /home/dotwaffle/go/pkg/mod/golang.org/x/sys@v0.42.0/unix/zerrors_freebsd_amd64.go
// (the keys are FreeBSD errno numbers; the values are Linux wire equivalents
// declared in errno.go).
func ErrnoFromUnix(e unix.Errno) Errno {
	if e == 0 {
		return 0
	}
	if v, ok := freebsdToLinuxErrno[e]; ok {
		return v
	}
	if e <= 34 {
		// 1..34 are POSIX-stable across Linux and FreeBSD.
		return Errno(e)
	}
	return EIO
}

// freebsdToLinuxErrno maps FreeBSD errno values (the keys, as syscall.Errno
// numbers on FreeBSD) to their Linux wire equivalents. EWOULDBLOCK aliases
// EAGAIN on FreeBSD (both = 35); the EWOULDBLOCK key is the only one used to
// avoid a duplicate-map-key compile error.
var freebsdToLinuxErrno = map[unix.Errno]Errno{
	unix.EWOULDBLOCK:     EAGAIN,          // 35 -> 11 (EAGAIN aliases EWOULDBLOCK on FreeBSD)
	unix.EINPROGRESS:     EINPROGRESS,     // 36 -> 115
	unix.EALREADY:        EALREADY,        // 37 -> 114
	unix.ENOTSOCK:        ENOTSOCK,        // 38 -> 88
	unix.EDESTADDRREQ:    EDESTADDRREQ,    // 39 -> 89
	unix.EMSGSIZE:        EMSGSIZE,        // 40 -> 90
	unix.EPROTOTYPE:      EPROTOTYPE,      // 41 -> 91
	unix.ENOPROTOOPT:     ENOPROTOOPT,     // 42 -> 92
	unix.EPROTONOSUPPORT: EPROTONOSUPPORT, // 43 -> 93
	unix.ESOCKTNOSUPPORT: ESOCKTNOSUPPORT, // 44 -> 94
	unix.EOPNOTSUPP:      ENOTSUP,         // 45 -> 95 (ENOTSUP aliases EOPNOTSUPP on FreeBSD)
	unix.EPFNOSUPPORT:    EPFNOSUPPORT,    // 46 -> 96
	unix.EAFNOSUPPORT:    EAFNOSUPPORT,    // 47 -> 97
	unix.EADDRINUSE:      EADDRINUSE,      // 48 -> 98
	unix.EADDRNOTAVAIL:   EADDRNOTAVAIL,   // 49 -> 99
	unix.ENETDOWN:        ENETDOWN,        // 50 -> 100
	unix.ENETUNREACH:     ENETUNREACH,     // 51 -> 101
	unix.ENETRESET:       ENETRESET,       // 52 -> 102
	unix.ECONNABORTED:    ECONNABORTED,    // 53 -> 103
	unix.ECONNRESET:      ECONNRESET,      // 54 -> 104
	unix.ENOBUFS:         ENOBUFS,         // 55 -> 105
	unix.EISCONN:         EISCONN,         // 56 -> 106
	unix.ENOTCONN:        ENOTCONN,        // 57 -> 107
	unix.ESHUTDOWN:       ESHUTDOWN,       // 58 -> 108
	unix.ETOOMANYREFS:    ETOOMANYREFS,    // 59 -> 109
	unix.ETIMEDOUT:       ETIMEDOUT,       // 60 -> 110
	unix.ECONNREFUSED:    ECONNREFUSED,    // 61 -> 111
	unix.ELOOP:           ELOOP,           // 62 -> 40
	unix.ENAMETOOLONG:    ENAMETOOLONG,    // 63 -> 36
	unix.EHOSTDOWN:       EHOSTDOWN,       // 64 -> 112
	unix.EHOSTUNREACH:    EHOSTUNREACH,    // 65 -> 113
	unix.ENOTEMPTY:       ENOTEMPTY,       // 66 -> 39
	// EPROCLIM (67) is FreeBSD-only -> falls through to EIO.
	unix.EUSERS:  EUSERS,  // 68 -> 87
	unix.EDQUOT:  EDQUOT,  // 69 -> 122
	unix.ESTALE:  ESTALE,  // 70 -> 116
	unix.EREMOTE: EREMOTE, // 71 -> 66
	// EBADRPC (72), ERPCMISMATCH (73), EPROGUNAVAIL (74), EPROGMISMATCH (75),
	// EPROCUNAVAIL (76) are FreeBSD-only -> fall through to EIO.
	unix.ENOLCK: ENOLCK, // 77 -> 37
	unix.ENOSYS: ENOSYS, // 78 -> 38
	// EFTYPE (79), EAUTH (80), ENEEDAUTH (81) are FreeBSD-only -> fall through.
	unix.EIDRM:     EIDRM,     // 82 -> 43
	unix.ENOMSG:    ENOMSG,    // 83 -> 42
	unix.EOVERFLOW: EOVERFLOW, // 84 -> 75
	unix.ECANCELED: ECANCELED, // 85 -> 125
	unix.EILSEQ:    EILSEQ,    // 86 -> 84
	// ENOATTR (87) is FreeBSD-only; xattr "attribute not found". Falls through
	// to EIO; could later be mapped to ENODATA (61) if client compatibility
	// requires it -- see follow-ups.
	// EDOOFUS (88) is FreeBSD-only -> falls through.
	unix.EBADMSG:   EBADMSG,   // 89 -> 74
	unix.EMULTIHOP: EMULTIHOP, // 90 -> 72
	unix.ENOLINK:   ENOLINK,   // 91 -> 67
	unix.EPROTO:    EPROTO,    // 92 -> 71
}

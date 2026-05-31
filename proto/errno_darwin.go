//go:build darwin

package proto

import "golang.org/x/sys/unix"

// ErrnoFromUnix converts a Darwin unix.Errno to its Linux wire equivalent.
// 9P2000.L defines errno numbers as the Linux UAPI values; without
// translation a macOS-hosted server would send BSD-numbered errnos (e.g.
// EAGAIN=35 instead of 11) that a Linux v9fs client misreads.
//
// Errnos 1..34 are POSIX-stable and shared between Linux and Darwin; they
// pass through unchanged. The darwinToLinuxErrno table covers errnos that
// diverge. Darwin-only errnos with no Linux equivalent (EPROCLIM, EBADRPC,
// ERPCMISMATCH, EPROGUNAVAIL, EPROGMISMATCH, EPROCUNAVAIL, EFTYPE, EAUTH,
// ENEEDAUTH, EPWROFF, EDEVERR, EBADEXEC, EBADARCH, ESHLIBVERS, EBADMACHO,
// ENOPOLICY, EQFULL) fall through to EIO.
//
// Darwin amd64 and arm64 share identical errno numbers (verified against
// golang.org/x/sys/unix zerrors_darwin_{amd64,arm64}.go). The FreeBSD table
// must not be reused: several high-range values diverge (e.g. EPROTO is 100
// on Darwin vs 92 on FreeBSD), and Darwin's EOPNOTSUPP (102) is a distinct
// number from ENOTSUP (45) where FreeBSD aliases the two.
func ErrnoFromUnix(e unix.Errno) Errno {
	if e == 0 {
		return 0
	}
	if v, ok := darwinToLinuxErrno[e]; ok {
		return v
	}
	if e <= 34 {
		// 1..34 are POSIX-stable across Linux and Darwin.
		return Errno(e)
	}
	return EIO
}

// darwinToLinuxErrno maps Darwin errno values (the keys) to their Linux wire
// equivalents (the values, declared in errno.go). EAGAIN aliases EWOULDBLOCK
// on Darwin (both 0x23); the EWOULDBLOCK key is the only one used to avoid a
// duplicate-map-key compile error.
var darwinToLinuxErrno = map[unix.Errno]Errno{
	unix.EWOULDBLOCK:     EAGAIN,          // 0x23 35 -> 11 (EAGAIN aliases EWOULDBLOCK)
	unix.EINPROGRESS:     EINPROGRESS,     // 0x24 36 -> 115
	unix.EALREADY:        EALREADY,        // 0x25 37 -> 114
	unix.ENOTSOCK:        ENOTSOCK,        // 0x26 38 -> 88
	unix.EDESTADDRREQ:    EDESTADDRREQ,    // 0x27 39 -> 89
	unix.EMSGSIZE:        EMSGSIZE,        // 0x28 40 -> 90
	unix.EPROTOTYPE:      EPROTOTYPE,      // 0x29 41 -> 91
	unix.ENOPROTOOPT:     ENOPROTOOPT,     // 0x2a 42 -> 92
	unix.EPROTONOSUPPORT: EPROTONOSUPPORT, // 0x2b 43 -> 93
	unix.ESOCKTNOSUPPORT: ESOCKTNOSUPPORT, // 0x2c 44 -> 94
	unix.ENOTSUP:         ENOTSUP,         // 0x2d 45 -> 95
	unix.EPFNOSUPPORT:    EPFNOSUPPORT,    // 0x2e 46 -> 96
	unix.EAFNOSUPPORT:    EAFNOSUPPORT,    // 0x2f 47 -> 97
	unix.EADDRINUSE:      EADDRINUSE,      // 0x30 48 -> 98
	unix.EADDRNOTAVAIL:   EADDRNOTAVAIL,   // 0x31 49 -> 99
	unix.ENETDOWN:        ENETDOWN,        // 0x32 50 -> 100
	unix.ENETUNREACH:     ENETUNREACH,     // 0x33 51 -> 101
	unix.ENETRESET:       ENETRESET,       // 0x34 52 -> 102
	unix.ECONNABORTED:    ECONNABORTED,    // 0x35 53 -> 103
	unix.ECONNRESET:      ECONNRESET,      // 0x36 54 -> 104
	unix.ENOBUFS:         ENOBUFS,         // 0x37 55 -> 105
	unix.EISCONN:         EISCONN,         // 0x38 56 -> 106
	unix.ENOTCONN:        ENOTCONN,        // 0x39 57 -> 107
	unix.ESHUTDOWN:       ESHUTDOWN,       // 0x3a 58 -> 108
	unix.ETOOMANYREFS:    ETOOMANYREFS,    // 0x3b 59 -> 109
	unix.ETIMEDOUT:       ETIMEDOUT,       // 0x3c 60 -> 110
	unix.ECONNREFUSED:    ECONNREFUSED,    // 0x3d 61 -> 111
	unix.ELOOP:           ELOOP,           // 0x3e 62 -> 40
	unix.ENAMETOOLONG:    ENAMETOOLONG,    // 0x3f 63 -> 36
	unix.EHOSTDOWN:       EHOSTDOWN,       // 0x40 64 -> 112
	unix.EHOSTUNREACH:    EHOSTUNREACH,    // 0x41 65 -> 113
	unix.ENOTEMPTY:       ENOTEMPTY,       // 0x42 66 -> 39
	// EPROCLIM (0x43 67) is Darwin-only -> falls through to EIO.
	unix.EUSERS:  EUSERS,  // 0x44 68 -> 87
	unix.EDQUOT:  EDQUOT,  // 0x45 69 -> 122
	unix.ESTALE:  ESTALE,  // 0x46 70 -> 116
	unix.EREMOTE: EREMOTE, // 0x47 71 -> 66
	// EBADRPC (72), ERPCMISMATCH (73), EPROGUNAVAIL (74), EPROGMISMATCH (75),
	// EPROCUNAVAIL (76) are Darwin-only -> fall through to EIO.
	unix.ENOLCK: ENOLCK, // 0x4d 77 -> 37
	unix.ENOSYS: ENOSYS, // 0x4e 78 -> 38
	// EFTYPE (0x4f 79) is Darwin-only -> falls through to EIO.
	unix.EOVERFLOW: EOVERFLOW, // 0x54 84 -> 75
	// EBADEXEC (0x55), EBADARCH (0x56), ESHLIBVERS (0x57), EBADMACHO (0x58)
	// are Darwin-only -> fall through to EIO.
	unix.ECANCELED:  ECANCELED, // 0x59 89 -> 125
	unix.EIDRM:      EIDRM,     // 0x5a 90 -> 43
	unix.ENOMSG:     ENOMSG,    // 0x5b 91 -> 42
	unix.EILSEQ:     EILSEQ,    // 0x5c 92 -> 84
	unix.ENOATTR:    ENODATA,   // 0x5d 93 -> 61 (xattr "attribute not found")
	unix.EBADMSG:    EBADMSG,   // 0x5e 94 -> 74
	unix.EMULTIHOP:  EMULTIHOP, // 0x5f 95 -> 72
	unix.ENOLINK:    ENOLINK,   // 0x61 97 -> 67
	unix.EPROTO:     EPROTO,    // 0x64 100 -> 71
	unix.EOPNOTSUPP: ENOTSUP,   // 0x66 102 -> 95 (distinct from ENOTSUP on Darwin)
	// ENOPOLICY (0x67 103), EQFULL (0x6a 106), EPWROFF, and EDEVERR are
	// Darwin-only -> fall through to EIO.
}

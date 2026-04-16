//go:build freebsd && !riscv64

package passthrough

// oPath supplies O_PATH on FreeBSD architectures where golang.org/x/sys
// hasn't yet exported it. Value matches FreeBSD <sys/fcntl.h> and is
// architecture-independent on FreeBSD 14+.
//
// TODO: remove once golang.org/x/sys exports unix.O_PATH for all FreeBSD
// architectures (currently only freebsd/riscv64 exports it as of v0.42.0).
const oPath = 0x400000

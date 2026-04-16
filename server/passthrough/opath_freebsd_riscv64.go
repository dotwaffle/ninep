//go:build freebsd && riscv64

package passthrough

import "golang.org/x/sys/unix"

// oPath is the O_PATH open(2) flag. golang.org/x/sys exports unix.O_PATH for
// freebsd/riscv64 only (as of v0.42.0); other FreeBSD architectures use the
// numeric constant from <sys/fcntl.h>.
const oPath = unix.O_PATH

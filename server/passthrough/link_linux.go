//go:build linux

package passthrough

import (
	"strconv"

	"golang.org/x/sys/unix"
)

func (n *Node) linkResolved(dstFD int, name string) error {
	path := "/proc/self/fd/" + strconv.Itoa(n.fd)
	return unix.Linkat(unix.AT_FDCWD, path, dstFD, name, unix.AT_SYMLINK_FOLLOW)
}

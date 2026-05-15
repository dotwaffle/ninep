//go:build linux

package passthrough

import (
	"strconv"

	"golang.org/x/sys/unix"
)

func (n *Node) openResolved(flags uint32) (int, error) {
	path := "/proc/self/fd/" + strconv.Itoa(n.fd)
	return unix.Open(path, openFlags(flags), 0)
}

func (n *Node) chmodResolved(mode uint32) error {
	if err := unix.Fchmodat(n.fd, "", mode, unix.AT_EMPTY_PATH); err == nil {
		return nil
	} else if err != unix.EOPNOTSUPP {
		return err
	}

	fd, err := n.openResolved(unix.O_RDONLY)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	return unix.Fchmod(fd, mode)
}

func (n *Node) truncateResolved(size uint64) error {
	const maxInt64 = uint64(1<<63 - 1)
	if size > maxInt64 {
		return unix.EFBIG
	}

	fd, err := n.openResolved(unix.O_WRONLY)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	return unix.Ftruncate(fd, int64(size))
}

func openFlags(flags uint32) int {
	return int(flags)&^unix.O_NOFOLLOW | unix.O_CLOEXEC
}

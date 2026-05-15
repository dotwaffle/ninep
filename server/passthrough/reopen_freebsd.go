//go:build freebsd

package passthrough

import "golang.org/x/sys/unix"

func (n *Node) openResolved(flags uint32) (int, error) {
	if n.parentFd == 0 && n.name == "" {
		if n.root == nil || n.root.hostPath == "" {
			return -1, unix.EINVAL
		}
		return unix.Open(n.root.hostPath, openFlags(flags), 0)
	}
	return unix.Openat(n.parentFd, n.name, openFlags(flags), 0)
}

func (n *Node) chmodResolved(mode uint32) error {
	if err := unix.Fchmod(n.fd, mode); err == nil {
		return nil
	} else if err != unix.EBADF {
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

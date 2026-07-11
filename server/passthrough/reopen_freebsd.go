//go:build freebsd

package passthrough

import (
	"errors"

	"golang.org/x/sys/unix"
)

func (n *Node) openResolved(flags uint32) (int, error) {
	if n.parentFd == 0 && n.name == "" {
		return unix.FcntlInt(uintptr(n.fd), unix.F_DUPFD_CLOEXEC, 0)
	}

	// This is a fresh name lookup in parentFd, not a reopen of the fd this
	// Node was originally resolved from (FreeBSD has no /proc/self/fd
	// equivalent for that -- see reopen_linux.go). A concurrent
	// rename/symlink-swap of the directory entry between the original
	// Lookup and this call could otherwise substitute an attacker-chosen
	// target. O_NOFOLLOW rejects a symlink substitution outright; the
	// (dev, ino) check below catches a same-type substitution (e.g. the
	// entry unlinked and replaced with a different regular file).
	fd, err := unix.Openat(n.parentFd, n.name, openFlags(flags)|unix.O_NOFOLLOW, 0)
	// FreeBSD reports EMLINK when O_NOFOLLOW rejects a final symlink.
	// Normalize it to the POSIX ELOOP contract used by passthrough callers.
	if errors.Is(err, unix.EMLINK) {
		return -1, unix.ELOOP
	}
	if err != nil {
		return -1, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	if uint64(st.Dev) != n.dev || st.Ino != n.QID().Path {
		_ = unix.Close(fd)
		return -1, unix.ESTALE
	}
	return fd, nil
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

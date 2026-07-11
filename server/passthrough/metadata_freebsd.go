//go:build freebsd

package passthrough

import "golang.org/x/sys/unix"

func (n *Node) chownResolved(uid, gid int) error {
	if n.parentFd == 0 && n.name == "" {
		return unix.Fchown(n.fd, uid, gid)
	}
	fd, err := n.openResolved(unix.O_RDONLY)
	if err == nil {
		defer func() { _ = unix.Close(fd) }()
		return unix.Fchown(fd, uid, gid)
	}
	if err != unix.ELOOP {
		return err
	}
	if err := n.verifyNamedIdentity(); err != nil {
		return err
	}
	return unix.Fchownat(n.parentFd, n.name, uid, gid, unix.AT_SYMLINK_NOFOLLOW)
}

func (n *Node) readlinkResolved() (string, error) {
	if n.parentFd == 0 && n.name == "" {
		return "", unix.EINVAL
	}
	if err := n.verifyNamedIdentity(); err != nil {
		return "", err
	}
	buf := make([]byte, 4096)
	nn, err := unix.Readlinkat(n.parentFd, n.name, buf)
	if err != nil {
		return "", err
	}
	return string(buf[:nn]), nil
}

func (n *Node) verifyNamedIdentity() error {
	var st unix.Stat_t
	if err := unix.Fstatat(n.parentFd, n.name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if uint64(st.Dev) != n.dev || st.Ino != n.ino {
		return unix.ESTALE
	}
	return nil
}

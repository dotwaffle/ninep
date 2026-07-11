//go:build linux

package passthrough

import "golang.org/x/sys/unix"

func (n *Node) chownResolved(uid, gid int) error {
	return unix.Fchownat(n.fd, "", uid, gid, unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW)
}

func (n *Node) readlinkResolved() (string, error) {
	buf := make([]byte, 4096)
	nn, err := unix.Readlinkat(n.fd, "", buf)
	if err != nil {
		return "", err
	}
	return string(buf[:nn]), nil
}

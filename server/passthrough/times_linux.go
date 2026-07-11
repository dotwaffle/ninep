//go:build linux

package passthrough

import "golang.org/x/sys/unix"

func (n *Node) setTimesResolved(ts []unix.Timespec) error {
	return unix.UtimesNanoAt(n.fd, "", ts, unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW)
}

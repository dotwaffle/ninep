//go:build freebsd

package passthrough

import "golang.org/x/sys/unix"

func (n *Node) setTimesResolved(ts []unix.Timespec) error {
	fd := n.fd
	closeFD := false
	if n.parentFd != 0 || n.name != "" {
		var err error
		fd, err = n.openResolved(unix.O_RDONLY)
		if err != nil {
			if err == unix.ELOOP {
				if err := n.verifyNamedIdentity(); err != nil {
					return err
				}
				return unix.UtimesNanoAt(n.parentFd, n.name, ts, unix.AT_SYMLINK_NOFOLLOW)
			}
			return err
		}
		closeFD = true
	}
	if closeFD {
		defer func() { _ = unix.Close(fd) }()
	}

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	resolved := [2]unix.Timespec{ts[0], ts[1]}
	if resolved[0].Nsec == unix.UTIME_OMIT {
		resolved[0] = st.Atim
	}
	if resolved[1].Nsec == unix.UTIME_OMIT {
		resolved[1] = st.Mtim
	}
	tv := []unix.Timeval{
		unix.NsecToTimeval(resolved[0].Nano()),
		unix.NsecToTimeval(resolved[1].Nano()),
	}
	return unix.Futimes(fd, tv)
}

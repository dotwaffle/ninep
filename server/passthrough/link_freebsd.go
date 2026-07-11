//go:build freebsd

package passthrough

import "golang.org/x/sys/unix"

func (n *Node) linkResolved(dstFD int, name string) error {
	if n.parentFd == 0 || n.name == "" {
		return unix.EINVAL
	}
	if err := n.verifyNamedIdentity(); err != nil {
		return err
	}
	return unix.Linkat(n.parentFd, n.name, dstFD, name, 0)
}

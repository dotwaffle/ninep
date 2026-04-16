//go:build linux

package passthrough

import (
	"bytes"
	"context"

	"golang.org/x/sys/unix"

	"github.com/dotwaffle/ninep/server"
)

// Compile-time interface assertions for xattr operations.
var (
	_ server.NodeXattrGetter  = (*Node)(nil)
	_ server.NodeXattrSetter  = (*Node)(nil)
	_ server.NodeXattrLister  = (*Node)(nil)
	_ server.NodeXattrRemover = (*Node)(nil)
)

// GetXattr reads an extended attribute value using Fgetxattr. It uses a
// retry-with-exact-size pattern to handle ERANGE: an initial 256-byte buffer
// is tried first, and if the value is larger, a second call with the exact
// size is made.
func (n *Node) GetXattr(_ context.Context, name string) ([]byte, error) {
	// First attempt with small buffer.
	buf := make([]byte, 256)
	sz, err := unix.Fgetxattr(n.fd, name, buf)
	if err == unix.ERANGE {
		// Query exact size.
		sz, err = unix.Fgetxattr(n.fd, name, nil)
		if err != nil {
			return nil, toProtoErr(err)
		}
		buf = make([]byte, sz)
		sz, err = unix.Fgetxattr(n.fd, name, buf)
	}
	if err != nil {
		return nil, toProtoErr(err)
	}
	return buf[:sz], nil
}

// SetXattr sets an extended attribute value using Fsetxattr.
func (n *Node) SetXattr(_ context.Context, name string, data []byte, flags uint32) error {
	if err := unix.Fsetxattr(n.fd, name, data, int(flags)); err != nil {
		return toProtoErr(err)
	}
	return nil
}

// ListXattrs lists all extended attribute names using Flistxattr.
// The kernel returns null-separated names.
func (n *Node) ListXattrs(_ context.Context) ([]string, error) {
	// Query size first.
	sz, err := unix.Flistxattr(n.fd, nil)
	if err != nil {
		return nil, toProtoErr(err)
	}
	if sz == 0 {
		return nil, nil
	}

	buf := make([]byte, sz)
	sz, err = unix.Flistxattr(n.fd, buf)
	if err != nil {
		return nil, toProtoErr(err)
	}

	// Split on null bytes, removing trailing empty string.
	parts := bytes.Split(buf[:sz], []byte{0})
	var names []string
	for _, p := range parts {
		if len(p) > 0 {
			names = append(names, string(p))
		}
	}
	return names, nil
}

// RemoveXattr removes an extended attribute using Fremovexattr.
func (n *Node) RemoveXattr(_ context.Context, name string) error {
	if err := unix.Fremovexattr(n.fd, name); err != nil {
		return toProtoErr(err)
	}
	return nil
}

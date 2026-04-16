//go:build freebsd

package passthrough

import (
	"context"
	"errors"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

// Compile-time interface assertions for xattr operations.
var (
	_ server.NodeXattrGetter  = (*Node)(nil)
	_ server.NodeXattrSetter  = (*Node)(nil)
	_ server.NodeXattrLister  = (*Node)(nil)
	_ server.NodeXattrRemover = (*Node)(nil)
)

// splitXattrName decomposes a 9P xattr name like "user.foo" or
// "system.posix_acl_access" into a FreeBSD namespace constant + bare name.
// Returns ok=false for unrecognized prefixes (caller should return ENOTSUP).
func splitXattrName(qualified string) (ns int, name string, ok bool) {
	if rest, after := strings.CutPrefix(qualified, "user."); after {
		return unix.EXTATTR_NAMESPACE_USER, rest, true
	}
	if rest, after := strings.CutPrefix(qualified, "system."); after {
		return unix.EXTATTR_NAMESPACE_SYSTEM, rest, true
	}
	// "trusted.*", "security.*", or unknown -> caller returns ENOTSUP.
	return 0, "", false
}

// extattrBufPtr returns a uintptr suitable for the Extattr*Fd data parameter,
// or 0 for an empty slice (which the syscall interprets as "size query").
func extattrBufPtr(b []byte) uintptr {
	if len(b) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&b[0]))
}

// GetXattr reads an extended attribute value via ExtattrGetFd.
//
// FreeBSD returns the required size when called with nbytes == 0; use the
// size-then-read pattern (no ERANGE).
func (n *Node) GetXattr(_ context.Context, name string) ([]byte, error) {
	ns, bare, ok := splitXattrName(name)
	if !ok {
		return nil, proto.ENOTSUP
	}
	sz, err := unix.ExtattrGetFd(n.fd, ns, bare, 0, 0)
	if err != nil {
		return nil, toProtoErr(err)
	}
	if sz == 0 {
		return nil, nil
	}
	buf := make([]byte, sz)
	got, err := unix.ExtattrGetFd(n.fd, ns, bare, extattrBufPtr(buf), len(buf))
	if err != nil {
		return nil, toProtoErr(err)
	}
	return buf[:got], nil
}

// SetXattr sets an extended attribute value via ExtattrSetFd.
//
// FreeBSD's extattr_set_fd has no XATTR_CREATE/XATTR_REPLACE flags; the flags
// argument from 9P is ignored on FreeBSD.
func (n *Node) SetXattr(_ context.Context, name string, data []byte, _ uint32) error {
	ns, bare, ok := splitXattrName(name)
	if !ok {
		return proto.ENOTSUP
	}
	if _, err := unix.ExtattrSetFd(n.fd, ns, bare, extattrBufPtr(data), len(data)); err != nil {
		return toProtoErr(err)
	}
	return nil
}

// ListXattrs lists all extended attribute names across USER and SYSTEM
// namespaces, prepending the appropriate prefix to each.
//
// FreeBSD's ExtattrListFd returns a packed list: [len:1][name:len]... with
// no NUL separators (per extattr_get_file(2)).
func (n *Node) ListXattrs(_ context.Context) ([]string, error) {
	var names []string
	for _, ent := range []struct {
		ns     int
		prefix string
	}{
		{unix.EXTATTR_NAMESPACE_USER, "user."},
		{unix.EXTATTR_NAMESPACE_SYSTEM, "system."},
	} {
		sz, err := unix.ExtattrListFd(n.fd, ent.ns, 0, 0)
		if err != nil {
			// SYSTEM namespace requires elevated privileges; EPERM is
			// expected for unprivileged servers. Skip gracefully.
			if errno, ok := errors.AsType[unix.Errno](err); ok && errno == unix.EPERM {
				continue
			}
			return nil, toProtoErr(err)
		}
		if sz == 0 {
			continue
		}
		buf := make([]byte, sz)
		got, err := unix.ExtattrListFd(n.fd, ent.ns, extattrBufPtr(buf), len(buf))
		if err != nil {
			return nil, toProtoErr(err)
		}
		names = append(names, parseExtattrList(buf[:got], ent.prefix)...)
	}
	return names, nil
}

// parseExtattrList parses FreeBSD's length-prefixed extattr list format.
// Each entry is [len:1][name:len].
func parseExtattrList(buf []byte, prefix string) []string {
	var names []string
	for len(buf) > 0 {
		n := int(buf[0])
		if 1+n > len(buf) {
			break
		}
		names = append(names, prefix+string(buf[1:1+n]))
		buf = buf[1+n:]
	}
	return names
}

// RemoveXattr removes an extended attribute via ExtattrDeleteFd.
func (n *Node) RemoveXattr(_ context.Context, name string) error {
	ns, bare, ok := splitXattrName(name)
	if !ok {
		return proto.ENOTSUP
	}
	if err := unix.ExtattrDeleteFd(n.fd, ns, bare); err != nil {
		return toProtoErr(err)
	}
	return nil
}

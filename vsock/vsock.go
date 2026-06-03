// Package vsock provides AF_VSOCK (Linux VM sockets) listeners and
// connections for running a ninep server or client over the virtio-vsock
// transport, including same-host loopback.
//
// A vsock connection is an ordinary [net.Conn], so it plugs directly into
// [github.com/dotwaffle/ninep/server.Server.ServeConn] and
// [github.com/dotwaffle/ninep/client.Dial]; a vsock listener is a
// [net.Listener] for [github.com/dotwaffle/ninep/server.Server.Serve]. The
// package exists only to construct those values: the standard library's net
// package does not speak AF_VSOCK, and [net.FileConn] rejects the address
// family (golang/go#69769), so the file-descriptor wrapping lives here.
//
// Only Linux is supported. On other platforms [Listen] and [Dial] return
// [ErrUnsupported].
package vsock

import (
	"errors"
	"fmt"
)

// Well-known context IDs (CIDs). These mirror the kernel VMADDR_CID_*
// constants and are stable values.
const (
	// Hypervisor (CID 0) addresses the hypervisor.
	Hypervisor uint32 = 0
	// Local (CID 1) is the same-host loopback address: a connection to
	// Local reaches a listener on the same machine. It is served by the
	// vsock_loopback transport (Linux 5.6+) and is primarily useful for
	// tests.
	Local uint32 = 1
	// Host (CID 2) addresses the host from within a guest.
	Host uint32 = 2
)

// ErrUnsupported is returned by [Listen] and [Dial] on non-Linux platforms.
var ErrUnsupported = errors.New("vsock: AF_VSOCK is only supported on Linux")

// Addr is a vsock address (context ID and port). It implements [net.Addr].
type Addr struct {
	ContextID uint32
	Port      uint32
}

// Network returns "vsock".
func (a Addr) Network() string { return "vsock" }

// String returns the address as "vsock:CID.Port".
func (a Addr) String() string { return fmt.Sprintf("vsock:%d.%d", a.ContextID, a.Port) }

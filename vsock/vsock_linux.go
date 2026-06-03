//go:build linux

package vsock

import (
	"context"
	"errors"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// Listen creates a [net.Listener] bound to the given vsock port on
// VMADDR_CID_ANY, accepting connections addressed to any local CID
// (including [Local] loopback).
//
// A port of 0 requests a kernel-assigned ephemeral port. Note that on some
// kernels an ephemeral bind is privileged and returns EACCES for
// unprivileged callers, so pass an explicit port if you are not running
// with privilege.
func Listen(port uint32) (net.Listener, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, os.NewSyscallError("socket", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}); err != nil {
		_ = unix.Close(fd)
		return nil, os.NewSyscallError("bind", err)
	}
	if err := unix.Listen(fd, unix.SOMAXCONN); err != nil {
		_ = unix.Close(fd)
		return nil, os.NewSyscallError("listen", err)
	}
	file := os.NewFile(uintptr(fd), "vsock-listener")
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("vsock: invalid listener descriptor")
	}
	addr, err := localAddr(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return &Listener{file: file, addr: addr}, nil
}

// Listener is a vsock [net.Listener].
type Listener struct {
	file *os.File
	addr Addr
}

// Accept waits for and returns the next connection.
func (l *Listener) Accept() (net.Conn, error) {
	rc, err := l.file.SyscallConn()
	if err != nil {
		return nil, err
	}
	var (
		nfd  int
		rsa  unix.Sockaddr
		aerr error
	)
	// Read blocks on the runtime poller until the listen fd is readable,
	// then accepts. A false return retries on a spurious wakeup (EAGAIN).
	if pollErr := rc.Read(func(fd uintptr) bool {
		nfd, rsa, aerr = unix.Accept4(int(fd), unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC)
		return !errors.Is(aerr, unix.EAGAIN)
	}); pollErr != nil {
		return nil, pollErr
	}
	if aerr != nil {
		return nil, os.NewSyscallError("accept", aerr)
	}
	return newConn(nfd, l.addr, addrFromSockaddr(rsa))
}

// Close closes the listener and unblocks any pending Accept.
func (l *Listener) Close() error { return l.file.Close() }

// Addr returns the listener's bound address.
func (l *Listener) Addr() net.Addr { return l.addr }

// Dial connects to the vsock (contextID, port) endpoint and returns a
// [net.Conn]. Use [Local] as contextID for same-host loopback. The context
// bounds the connect: its cancellation or deadline aborts a pending connect.
func Dial(ctx context.Context, contextID, port uint32) (net.Conn, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, os.NewSyscallError("socket", err)
	}
	connErr := unix.Connect(fd, &unix.SockaddrVM{CID: contextID, Port: port})
	file := os.NewFile(uintptr(fd), "vsock")
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("vsock: invalid descriptor")
	}
	switch {
	case connErr == nil:
		// Connected immediately, which is typical for loopback.
	case errors.Is(connErr, unix.EINPROGRESS):
		if err := waitConnect(ctx, file); err != nil {
			_ = file.Close()
			return nil, err
		}
	default:
		_ = file.Close()
		return nil, os.NewSyscallError("connect", connErr)
	}
	local, err := localAddr(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return &conn{File: file, local: local, remote: Addr{ContextID: contextID, Port: port}}, nil
}

// aLongTimeAgo is a deadline in the past, used to force a pending poll to
// return immediately when the dial context is cancelled.
var aLongTimeAgo = time.Unix(1, 0)

// waitConnect waits for a non-blocking connect on file to complete, honoring
// the context's deadline and cancellation, then reports the SO_ERROR result.
func waitConnect(ctx context.Context, file *os.File) error {
	if dl, ok := ctx.Deadline(); ok {
		_ = file.SetWriteDeadline(dl)
		defer func() { _ = file.SetWriteDeadline(time.Time{}) }()
	}
	// Cancellation (or deadline) interrupts the poll below by forcing the
	// write deadline into the past.
	stop := context.AfterFunc(ctx, func() { _ = file.SetWriteDeadline(aLongTimeAgo) })
	defer stop()

	rc, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var soErr error
	if pollErr := rc.Write(func(fd uintptr) bool {
		// SO_ERROR carries the definitive connect result once it is set.
		v, gerr := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_ERROR)
		switch {
		case gerr != nil:
			soErr = gerr
			return true
		case v != 0:
			soErr = unix.Errno(v)
			return true
		}
		// SO_ERROR is clear, but RawConn.Write's first callback fires
		// immediately and may precede connect completion. On loopback the
		// connect often finishes before the fd is registered with the
		// edge-triggered poller, so a blind wait would miss the POLLOUT
		// edge and hang. getpeername succeeds only once connected; while
		// still pending it returns ENOTCONN, so wait for write-readiness.
		if _, perr := unix.Getpeername(int(fd)); errors.Is(perr, unix.ENOTCONN) {
			return false
		}
		return true
	}); pollErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return pollErr
	}
	if soErr != nil {
		return os.NewSyscallError("connect", soErr)
	}
	return nil
}

// conn adapts an AF_VSOCK *os.File to [net.Conn]. *os.File already provides
// Read, Write, Close, and the SetDeadline methods (poller-integrated because
// the fd is non-blocking); only the addresses are added here.
type conn struct {
	*os.File
	local  Addr
	remote Addr
}

func newConn(fd int, local, remote Addr) (net.Conn, error) {
	file := os.NewFile(uintptr(fd), "vsock")
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("vsock: invalid descriptor")
	}
	return &conn{File: file, local: local, remote: remote}, nil
}

func (c *conn) LocalAddr() net.Addr  { return c.local }
func (c *conn) RemoteAddr() net.Addr { return c.remote }

// localAddr returns the address bound to file via getsockname.
func localAddr(file *os.File) (Addr, error) {
	rc, err := file.SyscallConn()
	if err != nil {
		return Addr{}, err
	}
	var (
		addr Addr
		gerr error
	)
	if ctrlErr := rc.Control(func(fd uintptr) {
		sa, e := unix.Getsockname(int(fd))
		if e != nil {
			gerr = e
			return
		}
		addr = addrFromSockaddr(sa)
	}); ctrlErr != nil {
		return Addr{}, ctrlErr
	}
	if gerr != nil {
		return Addr{}, os.NewSyscallError("getsockname", gerr)
	}
	return addr, nil
}

func addrFromSockaddr(sa unix.Sockaddr) Addr {
	if vm, ok := sa.(*unix.SockaddrVM); ok {
		return Addr{ContextID: vm.CID, Port: vm.Port}
	}
	return Addr{}
}

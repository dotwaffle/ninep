//go:build !linux

package vsock

import (
	"context"
	"net"
)

// Listen returns [ErrUnsupported] on non-Linux platforms.
func Listen(port uint32) (net.Listener, error) {
	_ = port
	return nil, ErrUnsupported
}

// Dial returns [ErrUnsupported] on non-Linux platforms.
func Dial(ctx context.Context, contextID, port uint32) (net.Conn, error) {
	_, _, _ = ctx, contextID, port
	return nil, ErrUnsupported
}

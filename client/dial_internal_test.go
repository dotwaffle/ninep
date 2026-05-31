package client

import (
	"net"
	"testing"
)

// TestEnableKeepAlive checks that enableKeepAlive is a safe no-op on a non-TCP
// transport and applies cleanly to a real TCP connection.
func TestEnableKeepAlive(t *testing.T) {
	t.Parallel()

	// Non-TCP (net.Pipe): must not panic and must do nothing.
	cli, srv := net.Pipe()
	defer func() { _ = cli.Close() }()
	defer func() { _ = srv.Close() }()
	enableKeepAlive(cli)

	// Real TCP loopback: exercises the apply path.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("tcp listen unavailable in this environment: %v", err)
	}
	defer func() { _ = ln.Close() }()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Skipf("tcp dial unavailable: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if _, ok := conn.(*net.TCPConn); !ok {
		t.Fatalf("net.Dial(tcp) returned %T, want *net.TCPConn", conn)
	}
	enableKeepAlive(conn)
}

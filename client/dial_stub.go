// Plan 19-02 → Plan 19-03 bridging stub.
//
// TODO(plan-19-03): delete this file. Plan 19-03 Task 1 pre_flight runs
//
//	rm -f client/dial_stub.go
//
// before writing the real Conn/Dial in client/conn.go + client/dial.go.
package client

import (
	"context"
	"net"
)

// Conn is the bridging stub declaration. Plan 19-03 Task 1 replaces this
// with the real struct in client/conn.go.
type Conn struct{}

// Dial is the bridging stub. It returns (nil, nil) so pair_test.go can
// compile and smoke-test the helper's skip-path. Plan 19-03 Task 2 ships
// the real Dial.
func Dial(ctx context.Context, nc net.Conn, opts ...Option) (*Conn, error) {
	return nil, nil
}

// Close is the bridging stub. The real method lives in client/close.go
// after Plan 19-05 lands.
func (c *Conn) Close() error { return nil }

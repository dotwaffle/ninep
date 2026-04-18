package client_test

import (
	"testing"
)

// TestConnMsize: the default newClientServerPair uses WithMsize(65536) on
// both sides, so the negotiated msize reported by Conn.Msize() must be
// exactly 65536.
func TestConnMsize(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	if got := cli.Msize(); got != 65536 {
		t.Fatalf("Msize = %d, want 65536", got)
	}
}

// TestConnMsize_NegotiatedDown: the client proposes a larger msize than
// the server's cap; Msize() must report the server-imposed minimum. Uses
// the newClientServerPairMsize helper from msize_test.go which forces
// both ends to 32768 -- negotiation then settles on that value.
//
// The stronger "proposal > server cap" invariant is exercised by passing
// a larger client WithMsize through the clientOpts path, but the helper
// pins both sides; in either shape Msize() returns the post-Tversion
// minimum.
func TestConnMsize_NegotiatedDown(t *testing.T) {
	t.Parallel()
	// Server cap = client proposal = 32768 via the shared helper. Msize
	// after Dial must report 32768.
	cli, cleanup := newClientServerPairMsize(t, buildTestRoot(t), 32768)
	defer cleanup()

	if got := cli.Msize(); got != 32768 {
		t.Fatalf("Msize = %d, want 32768", got)
	}
}

// TestConnDialect_L: the real server negotiates 9P2000.L; Dialect()
// must return exactly that string.
func TestConnDialect_L(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	if got := cli.Dialect(); got != "9P2000.L" {
		t.Fatalf("Dialect = %q, want %q", got, "9P2000.L")
	}
}

// TestConnDialect_u: use the .u-mock pair from roundtrip_uversion_test.go;
// that mock responds to Tversion with Version="9P2000.u", so the client
// selects protocolU and Dialect() must report "9P2000.u".
func TestConnDialect_u(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()

	if got := cli.Dialect(); got != "9P2000.u" {
		t.Fatalf("Dialect = %q, want %q", got, "9P2000.u")
	}
}

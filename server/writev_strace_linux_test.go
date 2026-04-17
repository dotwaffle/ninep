//go:build linux

package server

import "testing"

// TestPayloaderUsesWritev is a stub that will be implemented to re-exec
// the test binary under strace -e trace=writev and assert at least one
// writev(..., 2|3) syscall was emitted by the Rread response path.
func TestPayloaderUsesWritev(t *testing.T) {
	t.Fatal("not implemented")
}

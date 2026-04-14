package server

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"

	"github.com/dotwaffle/ninep/proto"
)

// TestErrnoFromError verifies errnoFromError bridges both proto.Errno and
// syscall.Errno (direct, wrapped, and via os.PathError). Linux UAPI errno
// values 1..133 match proto.Errno exactly, so numeric-cast bridging is safe.
// Unknown errors default to proto.EIO. Nil error also returns proto.EIO.
func TestErrnoFromError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   error
		want proto.Errno
	}{
		{"proto_errno", proto.ENOENT, proto.ENOENT},
		{"syscall_errno_direct", syscall.ENOTEMPTY, proto.Errno(39)},
		{"syscall_errno_wrapped", fmt.Errorf("wrap: %w", syscall.EROFS), proto.EROFS},
		{"os_path_error", &os.PathError{Op: "open", Path: "/x", Err: syscall.ENOENT}, proto.ENOENT},
		{"unknown", errors.New("wat"), proto.EIO},
		{"nil", nil, proto.EIO},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := errnoFromError(c.in)
			if got != c.want {
				t.Errorf("errnoFromError(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

package passthrough

import (
	"context"
	"syscall"

	"github.com/dotwaffle/ninep/server"
)

// fileHandle wraps an OS file descriptor for per-open read/write operations
// using Pread/Pwrite for offset-based I/O without shared seek position.
type fileHandle struct {
	fd int
}

// Compile-time assertions that fileHandle implements the server file handle interfaces.
var (
	_ server.FileReader   = (*fileHandle)(nil)
	_ server.FileWriter   = (*fileHandle)(nil)
	_ server.FileReleaser = (*fileHandle)(nil)
)

// Read reads up to count bytes starting at offset using Pread.
func (h *fileHandle) Read(_ context.Context, offset uint64, count uint32) ([]byte, error) {
	buf := make([]byte, count)
	n, err := syscall.Pread(h.fd, buf, int64(offset))
	if err != nil {
		return nil, toProtoErr(err)
	}
	return buf[:n], nil
}

// Write writes data at the given offset using Pwrite and returns the count
// of bytes written.
func (h *fileHandle) Write(_ context.Context, data []byte, offset uint64) (uint32, error) {
	n, err := syscall.Pwrite(h.fd, data, int64(offset))
	if err != nil {
		return 0, toProtoErr(err)
	}
	return uint32(n), nil
}

// Release closes the OS file descriptor.
func (h *fileHandle) Release(_ context.Context) error {
	return toProtoErr(syscall.Close(h.fd))
}

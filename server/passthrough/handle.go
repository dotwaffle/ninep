//go:build linux || freebsd

package passthrough

import (
	"context"

	"golang.org/x/sys/unix"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

// fileHandle is declared in types.go (gated linux || freebsd) so the same
// struct backs both ports.

// Compile-time assertions that fileHandle implements the server file handle interfaces.
var (
	_ server.FileReader       = (*fileHandle)(nil)
	_ server.FileWriter       = (*fileHandle)(nil)
	_ server.FileReleaser     = (*fileHandle)(nil)
	_ server.FileSyncer       = (*fileHandle)(nil)
	_ server.FileRawReaddirer = (*dirHandle)(nil)
	_ server.FileReleaser     = (*dirHandle)(nil)
)

// Read reads up to len(buf) bytes starting at offset using Pread.
func (h *fileHandle) Read(_ context.Context, buf []byte, offset uint64) (int, error) {
	n, err := unix.Pread(h.fd, buf, int64(offset))
	if err != nil {
		return 0, toProtoErr(err)
	}
	return n, nil
}

// Write writes data at the given offset using Pwrite and returns the count
// of bytes written.
func (h *fileHandle) Write(_ context.Context, data []byte, offset uint64) (uint32, error) {
	n, err := unix.Pwrite(h.fd, data, int64(offset))
	if err != nil {
		return 0, toProtoErr(err)
	}
	return uint32(n), nil
}

// Release closes the OS file descriptor.
func (h *fileHandle) Release(_ context.Context) error {
	return toProtoErr(unix.Close(h.fd))
}

// Fsync flushes buffered writes on the open file to durable storage via
// unix.Fsync on the reopened fd. Returns a proto.Errno on failure.
func (h *fileHandle) Fsync(_ context.Context) error {
	return toProtoErr(unix.Fsync(h.fd))
}

// RawReaddir streams one bounded native directory chunk into the caller's 9P
// response buffer. The native offset cookie allows a later request to resume
// without retaining the directory listing in server memory.
func (h *dirHandle) RawReaddir(_ context.Context, buf []byte, offset uint64) (int, error) {
	const maxOffset = uint64(1<<63 - 1)
	if offset > maxOffset {
		return 0, proto.EINVAL
	}
	if _, err := unix.Seek(h.fd, int64(offset), 0); err != nil {
		return 0, toProtoErr(err)
	}
	var raw [8192]byte
	n, err := unix.Getdents(h.fd, raw[:])
	if err != nil {
		return 0, toProtoErr(err)
	}
	encoded, _ := proto.EncodeDirentsInto(buf, parseDirents(raw[:n]))
	return encoded, nil
}

func (h *dirHandle) Release(_ context.Context) error {
	return toProtoErr(unix.Close(h.fd))
}

package server

import (
	"context"

	"github.com/dotwaffle/ninep/proto"
)

// FileHandle is a marker interface for per-open state returned by NodeOpener.Open.
// Implement FileReader, FileWriter, FileReleaser, or FileReaddirer as needed.
type FileHandle any

// FileReader is implemented by file handles that support reading.
type FileReader interface {
	// Read reads up to count bytes starting at offset.
	Read(ctx context.Context, offset uint64, count uint32) ([]byte, error)
}

// FileWriter is implemented by file handles that support writing.
type FileWriter interface {
	// Write writes data at the given offset and returns the count of bytes written.
	Write(ctx context.Context, data []byte, offset uint64) (uint32, error)
}

// FileReleaser is implemented by file handles that need cleanup on clunk.
type FileReleaser interface {
	// Release releases resources associated with this file handle.
	Release(ctx context.Context) error
}

// FileReaddirer is implemented by file handles that support reading directory entries.
type FileReaddirer interface {
	// Readdir returns all directory entries for the open handle.
	Readdir(ctx context.Context) ([]proto.Dirent, error)
}

// FileRawReaddirer is implemented by file handles that manage their own
// readdir offset tracking.
type FileRawReaddirer interface {
	// RawReaddir returns raw dirent bytes for the given offset and count.
	RawReaddir(ctx context.Context, offset uint64, count uint32) ([]byte, error)
}

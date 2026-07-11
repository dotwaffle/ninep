package client

import "context"

// Fsync flushes this File's data (and, unless dataSync is true, its
// metadata) to storage. The File must have been opened via
// [Conn.OpenFile], [Conn.Create], or an equivalent Tlopen/Tlcreate
// path -- the server returns an error for a fid that has never been
// opened.
//
// dataSync mirrors the fdatasync(2)/fsync(2) distinction: true
// requests a data-only sync, false requests data and metadata.
// Implementations are permitted to always perform a full sync
// regardless of dataSync (matching this library's own server-side
// [server.FileSyncer]/[server.NodeFsyncer] handling).
//
// Requires a 9P2000.L-negotiated Conn. Returns [ErrNotSupported]
// (wrapped with op context) on a .u Conn before touching the wire.
func (f *File) Fsync(ctx context.Context, dataSync bool) error {
	return f.conn.Raw().Tfsync(ctx, f.fid, dataSync)
}

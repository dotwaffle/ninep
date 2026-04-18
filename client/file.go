package client

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/dotwaffle/ninep/proto"
)

// Compile-time assertions per D-16 (20-CONTEXT.md). Breakage fails
// `go build`, so the *File handle can never silently drift out of the
// io.* interface family.
var (
	_ io.Reader   = (*File)(nil)
	_ io.Writer   = (*File)(nil)
	_ io.Closer   = (*File)(nil)
	_ io.Seeker   = (*File)(nil)
	_ io.ReaderAt = (*File)(nil)
	_ io.WriterAt = (*File)(nil)
)

// File is a handle to a 9P file. Safe for sequential use; callers
// wanting parallel reads on the same server-side file should use
// [File.Clone] to obtain an independent handle with its own fid and
// offset.
//
// Obtain a File from [Conn.Attach] (root of the filesystem),
// [Conn.OpenFile] (opened file at a path), [Conn.Create] (created +
// opened file), [File.Walk] (navigated-to node, NOT opened), or
// [File.Clone] (duplicate of an existing File at the same position
// with a fresh fid and zero offset).
//
// # Close idempotency
//
// Unlike [os.File], which returns [os.ErrClosed] on a second Close,
// [File.Close] returns nil on every call after the first (D-06 in the
// Phase 20 CONTEXT.md). This simplifies defer-heavy code at the cost
// of masking double-close bugs. If a double-close diagnostic is needed
// in a future rev, flip the second-call return to [os.ErrClosed];
// callers tracked via errors.Is continue to compile.
//
// # Concurrency
//
// The per-File mutex (f.mu) serializes [File.Read], [File.Write],
// [File.ReadAt], and [File.WriteAt] so that offset mutation in the
// sequential path is race-free. [File.ReadAt] and [File.WriteAt]
// still conform to their [io.ReaderAt]/[io.WriterAt] contracts -- the
// contracts permit parallel CALLS and the implementation satisfies
// "does not panic or corrupt" by serializing. Callers that want
// actual parallel I/O spawn [File.Clone]s per goroutine; each clone
// has its own fid and mutex.
//
// [File.Close] does NOT take the File mutex -- a Close issued while a
// Read is in flight unblocks via the Conn's closeCh path rather than
// deadlocking on the handle mutex (Pitfall 7 in 20-RESEARCH.md §9).
type File struct {
	conn *Conn
	fid  proto.Fid
	qid  proto.QID // populated at construction; identifies the file
	// iounit is the server's suggested maximum single-op chunk size as
	// returned by Tlopen/Topen's Rlopen.IOUnit. Zero means "use msize";
	// File.Read/Write clamp to min(iounit, msize - frame overhead).
	iounit uint32

	mu         sync.Mutex // serializes Read/Write/ReadAt/WriteAt; guards offset + cachedSize
	offset     int64      // local seek offset per D-09
	cachedSize int64      // populated by File.Sync(); 0 = unknown (SeekEnd gate)

	closeOnce sync.Once // idempotent Close per D-06
	closeErr  error     // captured by the first Close; NOT returned on subsequent calls
}

// newFile constructs a *File. Internal helper used by session.go and
// file.go. Callers that already hold an acquired fid from
// conn.fids.acquire AND have issued a successful Tattach/Twalk/Tlopen
// against it use this to produce the handle.
func newFile(c *Conn, fid proto.Fid, qid proto.QID, iounit uint32) *File {
	return &File{
		conn:   c,
		fid:    fid,
		qid:    qid,
		iounit: iounit,
	}
}

// Qid returns the server's unique identifier for this file. Set at
// construction; does not issue a wire op.
func (f *File) Qid() proto.QID {
	return f.qid
}

// Fid returns the proto.Fid the File holds on the server. The fid is
// stable for the File's lifetime (Close releases it back to the
// allocator). Exposed for Raw-surface interop and for tests that need
// to probe the post-Close fid state.
func (f *File) Fid() proto.Fid {
	return f.fid
}

// Close clunks the fid on the server, releases the fid to the
// allocator's reuse cache, and marks the File as closed. Subsequent
// Close calls return nil without touching the wire (D-06).
//
// The error from the first Close (if Tclunk returned one) is captured
// into f.closeErr for diagnostics and returned; subsequent calls
// return nil. Close does not take f.mu -- a concurrent in-flight
// Read/Write on this File unblocks via the Conn's shutdown path
// (Conn.Close / Conn.Shutdown), not via the handle mutex.
func (f *File) Close() error {
	f.closeOnce.Do(func() {
		// Close uses a bounded ctx so a wedged Tclunk does not hang
		// the caller indefinitely. The Conn's drain deadline (5s per
		// Phase 19 D-22) is the correct ceiling -- longer than any
		// reasonable server response, shorter than a test timeout.
		ctx, cancel := context.WithTimeout(context.Background(), cleanupDeadline)
		defer cancel()
		err := f.conn.Clunk(ctx, f.fid)
		// Release AFTER Clunk returns (Pitfall 6): the Rclunk has
		// landed at this point, so the server-view is cleared and the
		// allocator can safely hand this fid to another caller.
		f.conn.fids.release(f.fid)
		f.closeErr = err
	})
	return f.closeErr
}

// Walk returns a new *File for the node reached by following names
// from this File's position. Does NOT open the returned File --
// useful for Stat (Phase 21) and ReadDir without opening.
//
// Empty names is invalid here (use [File.Clone] for 0-step walks).
// Callers passing empty names receive an error.
//
// On error, any reserved fid slot is released to the allocator before
// the error is returned (Pitfall 2).
func (f *File) Walk(ctx context.Context, names []string) (*File, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("client: File.Walk requires at least one name (use File.Clone for 0-step walk)")
	}
	newFid, err := f.conn.fids.acquire()
	if err != nil {
		return nil, err
	}
	qids, err := f.conn.Walk(ctx, f.fid, newFid, names)
	if err != nil {
		f.conn.fids.release(newFid)
		return nil, err
	}
	if len(qids) != len(names) {
		// Per 9P spec, Rwalk with len(qids) < len(names) means the
		// server stopped at qids[len-1] and newFid is NOT bound. Only
		// release; no Clunk needed.
		f.conn.fids.release(newFid)
		return nil, fmt.Errorf("client: partial walk (%d of %d steps)", len(qids), len(names))
	}
	lastQid := qids[len(qids)-1]
	return newFile(f.conn, newFid, lastQid, 0), nil
}

// Clone returns a new independent *File at the same server-side node
// via Twalk(oldFid, newFid, nil). The clone has its own fid, its own
// zero offset, and its own mutex. Closing the clone does not affect
// this File; closing this File does not affect the clone (D-13).
//
// On error, the reserved newFid is released to the allocator (Pitfall
// 2). A 0-step Walk binds newFid server-side only on success, so no
// Tclunk is needed on the error path.
func (f *File) Clone(ctx context.Context) (*File, error) {
	newFid, err := f.conn.fids.acquire()
	if err != nil {
		return nil, err
	}
	if _, err := f.conn.Walk(ctx, f.fid, newFid, nil); err != nil {
		f.conn.fids.release(newFid)
		return nil, err
	}
	clone := newFile(f.conn, newFid, f.qid, f.iounit)
	// offset stays at 0 (independent position per D-13). cachedSize
	// inherits from the parent -- the underlying server node is the
	// same, so the cache is still valid.
	f.mu.Lock()
	clone.cachedSize = f.cachedSize
	f.mu.Unlock()
	return clone, nil
}

// Read is implemented in Plan 20-04. This stub returns
// [ErrNotSupported] so the *File still satisfies [io.Reader] at build
// time.
//
// TODO(20-04): replace with the real Read implementation per
// 20-RESEARCH.md §io.Reader.
func (f *File) Read(p []byte) (int, error) {
	return 0, ErrNotSupported
}

// Write is implemented in Plan 20-04.
//
// TODO(20-04): replace with the real Write implementation.
func (f *File) Write(p []byte) (int, error) {
	return 0, ErrNotSupported
}

// Seek is implemented in Plan 20-04.
//
// TODO(20-04): replace with the real Seek implementation.
func (f *File) Seek(offset int64, whence int) (int64, error) {
	return 0, ErrNotSupported
}

// ReadAt is implemented in Plan 20-04.
//
// TODO(20-04): replace with the real ReadAt implementation.
func (f *File) ReadAt(p []byte, off int64) (int, error) {
	return 0, ErrNotSupported
}

// WriteAt is implemented in Plan 20-04.
//
// TODO(20-04): replace with the real WriteAt implementation.
func (f *File) WriteAt(p []byte, off int64) (int, error) {
	return 0, ErrNotSupported
}

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

	mu     sync.Mutex // serializes Read/Write/ReadAt/WriteAt; guards offset + cachedSize
	offset int64      // local seek offset per D-09
	// cachedSize is consulted by Seek(SeekEnd); populated by File.Sync
	// once Phase 21 ships Tgetattr/Tstat. Zero until then.
	cachedSize int64

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

// ioFrameOverhead is the per-Tread/Twrite wire overhead beyond the
// 7-byte message header. For Twrite it is fid[4] + offset[8] +
// count[4] = 16 bytes; for Rread it is count[4] = 4 bytes. Using 24
// as a single clamp constant covers both directions with a small
// safety margin and matches the Q5 resolution ("iounit=0 clamps to
// msize - 24").
const ioFrameOverhead uint32 = 24

// maxChunk returns the largest count the File should request in a
// single Tread or pass in a single Twrite. Clamps by min(iounit,
// msize - ioFrameOverhead). iounit==0 (server says "use msize")
// collapses to the msize-only clamp (Pitfall 9).
func (f *File) maxChunk() uint32 {
	msizeLimit := f.conn.Msize() - ioFrameOverhead
	if f.iounit == 0 {
		return msizeLimit
	}
	if f.iounit < msizeLimit {
		return f.iounit
	}
	return msizeLimit
}

// Read reads up to len(p) bytes from the File starting at the current
// local offset (see [File.Seek]). Returns io.EOF when the server
// responds with zero bytes on a non-empty p.
//
// Short reads are permitted by [io.Reader] -- each call issues at most
// one Tread, and the server may return fewer bytes than requested.
// Callers wanting "fill or error" semantics should use [File.ReadAt]
// or wrap with [bufio.Reader] / [io.ReadFull].
//
// Context: Read does NOT take a ctx -- the [io.Reader] contract has no
// ctx slot. Read uses a background context with no timeout. For
// cancellable I/O, shut down the Conn ([Conn.Close] / [Conn.Shutdown])
// which unblocks all in-flight reads with [ErrClosed].
//
// Thread safety: serialized by f.mu with [File.Write], [File.ReadAt],
// and [File.WriteAt]. Use [File.Clone] for parallel I/O.
func (f *File) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.offset < 0 {
		return 0, fmt.Errorf("client: negative offset %d", f.offset)
	}
	count := uint32(len(p))
	if m := f.maxChunk(); count > m {
		count = m
	}
	data, err := f.conn.Read(context.Background(), f.fid, uint64(f.offset), count)
	if err != nil {
		return 0, err
	}
	n := copy(p, data)
	f.offset += int64(n)
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

// Write writes len(p) bytes to the File starting at the current local
// offset, advancing the offset by bytes-written. Chunks the payload
// over multiple Twrites when len(p) exceeds min(iounit, msize -
// ioFrameOverhead).
//
// Returns [io.ErrShortWrite] if the server reports a Twrite count less
// than the chunk size sent -- per the [io.Writer] contract, a non-nil
// error must accompany any n < len(p) result.
//
// Thread safety: serialized with other I/O methods on the same *File
// via f.mu.
func (f *File) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.offset < 0 {
		return 0, fmt.Errorf("client: negative offset %d", f.offset)
	}
	total := 0
	for total < len(p) {
		chunk := p[total:]
		if m := f.maxChunk(); uint32(len(chunk)) > m {
			chunk = chunk[:m]
		}
		n, err := f.conn.Write(context.Background(), f.fid, uint64(f.offset), chunk)
		if err != nil {
			if total > 0 {
				return total, err
			}
			return 0, err
		}
		total += int(n)
		f.offset += int64(n)
		if int(n) < len(chunk) {
			// Server reported short write. io.Writer contract requires a
			// non-nil error alongside n < len(p); surface as
			// io.ErrShortWrite so callers can errors.Is against it.
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}

// Seek sets the local offset for the next [File.Read] or [File.Write]
// on this File per D-09. Does NOT issue a wire op -- 9P is
// offset-addressed on every Tread/Twrite, so there is no server-side
// seek state to synchronize.
//
// Whence:
//   - [io.SeekStart]:   offset is the absolute position.
//   - [io.SeekCurrent]: offset is relative to the current position.
//   - [io.SeekEnd]:     offset is relative to the file's size, read
//     from f.cachedSize. cachedSize defaults to 0 and is populated by
//     [File.Sync] once Phase 21 ships Tgetattr/Tstat; until then
//     SeekEnd(0) returns 0 (correct for an empty file) and
//     SeekEnd(-n) for n > 0 returns a "negative position" error with
//     guidance to call File.Sync first.
//
// Returns an error when the computed absolute position is negative.
// Seeking past the end of the file is allowed and does not error --
// subsequent Reads return [io.EOF], and subsequent Writes may extend
// the file (server-permitting).
//
// Seek on a directory fid succeeds (pure arithmetic). A subsequent
// [File.Read] on a directory fid surfaces the server's EISDIR (or
// equivalent) error; this matches [os.File] behavior per D-11.
//
// Thread safety: serialized with I/O methods via f.mu.
func (f *File) Seek(offset int64, whence int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = f.offset + offset
	case io.SeekEnd:
		abs = f.cachedSize + offset
	default:
		return f.offset, fmt.Errorf("client: invalid whence %d", whence)
	}
	if abs < 0 {
		if whence == io.SeekEnd && f.cachedSize == 0 {
			return f.offset, fmt.Errorf("client: negative position %d; SeekEnd requires File.Sync to populate size (Phase 21)", abs)
		}
		return f.offset, fmt.Errorf("client: negative position %d", abs)
	}
	f.offset = abs
	return abs, nil
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

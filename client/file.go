package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/dotwaffle/ninep/proto"
)

// Compile-time assertions. Breakage fails `go build`, so the *File
// handle can never silently drift out of the io.* interface family.
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
// [File.Close] returns nil on every call after the first. This
// simplifies defer-heavy code at the cost of masking double-close
// bugs. If a double-close diagnostic is needed in a future rev, flip
// the second-call return to [os.ErrClosed]; callers tracked via
// errors.Is continue to compile.
//
// # Concurrency
//
// Sequential Read, Write, Seek, and ReadDir calls share a state mutex.
// ReadAt and WriteAt do not touch that state and may run concurrently.
// A lifecycle lock prevents Close from retiring the fid while any file
// operation is using it; operations started after Close return [ErrClosed].
type File struct {
	conn *Conn
	fid  proto.Fid
	qid  proto.QID // populated at construction; identifies the file
	// iounit is the server's suggested maximum single-op chunk size as
	// returned by Tlopen/Topen's Rlopen.IOUnit. Zero means "use msize";
	// File.Read/Write clamp to min(iounit, msize - frame overhead).
	iounit uint32

	lifecycleMu sync.RWMutex
	closed      bool

	mu     sync.Mutex // serializes sequential state; guards offset + cachedSize + readdirOffset
	offset int64      // local seek offset
	// cachedSize is consulted by Seek(SeekEnd); populated by File.RefreshSize
	// via Tgetattr/Tstat.
	cachedSize int64
	// readdirOffset is the Treaddir Offset field for the NEXT
	// directory-enumeration round-trip. Populated from the final
	// proto.Dirent.Offset of each Rreaddir's packed data. Separate from
	// f.offset because directory enumeration uses server-provided
	// per-entry offsets rather than byte positions. Guarded by f.mu.
	readdirOffset uint64
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

func (f *File) beginOp() error {
	f.lifecycleMu.RLock()
	if f.closed {
		f.lifecycleMu.RUnlock()
		return ErrClosed
	}
	return nil
}

func (f *File) endOp() {
	f.lifecycleMu.RUnlock()
}

// Close clunks the fid on the server and marks the File as closed.
// Subsequent Close calls return nil without touching the wire. The fid
// number returns to the allocator's reuse cache only when the server's
// view is known to be clear: Rclunk landed, the server answered with an
// error (the fid is clunked even then), or the connection is down. After
// a flush or deadline expiry the number is leaked instead -- reusing it
// against a server that may still hold the fid would alias a new
// caller's Twalk onto the old binding.
//
// The first caller receives any cleanup error. Concurrent and later calls
// wait for that cleanup to finish and then return nil.
//
// Context: Close does NOT use [Conn.opCtx] / [WithRequestTimeout].
// Clunk is governed by [WithCleanupTimeout], not [WithRequestTimeout].
func (f *File) Close() error {
	f.lifecycleMu.Lock()
	defer f.lifecycleMu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	return f.conn.cleanupFid(f.fid)
}

// Walk returns a new *File for the node reached by following names
// from this File's position. Does NOT open the returned File --
// useful for Stat and ReadDir without opening.
//
// Empty names is invalid here (use [File.Clone] for 0-step walks).
// Callers passing empty names receive an error.
//
// On error, any reserved fid slot is released to the allocator before
// the error is returned.
func (f *File) Walk(ctx context.Context, names []string) (*File, error) {
	if err := f.beginOp(); err != nil {
		return nil, err
	}
	defer f.endOp()
	if len(names) == 0 {
		return nil, errors.New("client: File.Walk requires at least one name (use File.Clone for 0-step walk)")
	}
	newFid, err := f.conn.fids.acquire()
	if err != nil {
		return nil, err
	}
	qids, err := f.conn.twalk(ctx, f.fid, newFid, names)
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
// this File; closing this File does not affect the clone.
//
// On error, the reserved newFid is released to the allocator. A
// 0-step Walk binds newFid server-side only on success, so no Tclunk
// is needed on the error path.
func (f *File) Clone(ctx context.Context) (*File, error) {
	if err := f.beginOp(); err != nil {
		return nil, err
	}
	defer f.endOp()
	newFid, err := f.conn.fids.acquire()
	if err != nil {
		return nil, err
	}
	if _, err := f.conn.twalk(ctx, f.fid, newFid, nil); err != nil {
		f.conn.fids.release(newFid)
		return nil, err
	}
	clone := newFile(f.conn, newFid, f.qid, f.iounit)
	// offset stays at 0 (independent position). cachedSize
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
// safety margin.
const ioFrameOverhead uint32 = 24

// maxChunk returns the largest count the File should request in a
// single Tread or pass in a single Twrite. Clamps by min(iounit,
// msize - ioFrameOverhead, [proto.MaxDataSize]). iounit==0 (server says
// "use msize") collapses to the msize-only clamp. The proto.MaxDataSize
// cap only bites when a peer negotiates an msize above 16 MiB: a count
// past that ceiling would be rejected by the receiver's decoder and tear
// down the connection.
func (f *File) maxChunk() uint32 {
	msizeLimit := f.conn.Msize() - ioFrameOverhead
	if f.iounit != 0 && f.iounit < msizeLimit {
		msizeLimit = f.iounit
	}
	return min(msizeLimit, proto.MaxDataSize)
}

// chunkLen returns min(n, m) as a uint32. n is compared against m in
// int space BEFORE any conversion, so a buffer length past uint32's
// range (>4 GiB, reachable only on 64-bit platforms) clamps to m
// instead of silently wrapping to a small value -- uint32(n) directly
// would truncate the high bits first and could produce a count far
// smaller than the real chunk size.
func chunkLen(n int, m uint32) uint32 {
	if n > int(m) {
		return m
	}
	return uint32(n)
}

// ReadCtx is the ctx-taking variant of [File.Read]. Satisfies the same
// byte-slice semantics as [File.Read] (advances the local offset,
// clamps count to min(iounit, msize-overhead), returns [io.EOF] on a
// zero-byte server response) but honors the caller-supplied ctx
// verbatim - [WithRequestTimeout] (if set on the Conn) is IGNORED in
// favor of the caller's ctx.
//
// Internally uses the same zero-copy read path as [File.ReadAtCtx]:
// the Rread response is decoded directly into p, skipping both the
// intermediate Rread.Data allocation and the result-copy that
// [Raw.Tread] would otherwise pay.
//
// Serializes against other I/O methods on the same *File via f.mu.
// For parallel I/O, use [File.Clone] (which issues its own fid).
//
// On ctx cancellation or deadline expiry, a Tflush(oldtag) is sent via
// the shared roundTrip pipeline; the returned error
// satisfies [errors.Is] against ctx.Err() (Canceled or DeadlineExceeded)
// and, on the Rflush-first path, also against [ErrFlushed].
func (f *File) ReadCtx(ctx context.Context, p []byte) (int, error) {
	if err := f.beginOp(); err != nil {
		return 0, err
	}
	defer f.endOp()
	if len(p) == 0 {
		return 0, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.offset < 0 {
		return 0, fmt.Errorf("client: negative offset %d", f.offset)
	}
	count := chunkLen(len(p), f.maxChunk())
	n, err := f.conn.readAtZeroCopy(ctx, f.fid, uint64(f.offset), count, p[:count])
	if err != nil {
		return 0, err
	}
	f.offset += int64(n)
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

// Read reads up to len(p) bytes from the File starting at the current
// local offset (see [File.Seek]). Returns [io.EOF] when the server
// responds with zero bytes on a non-empty p.
//
// Short reads are permitted by [io.Reader] -- each call issues at most
// one Tread, and the server may return fewer bytes than requested.
// Callers wanting "fill or error" semantics should use [File.ReadAt]
// or wrap with [bufio.Reader] / [io.ReadFull].
//
// Context: Read does NOT take a ctx - the [io.Reader] contract has no
// ctx slot. Read derives its ctx from the Conn's [WithRequestTimeout]
// setting (default: infinite wait, matching Linux v9fs kernel
// parity). Callers that need per-op cancellation use [File.ReadCtx]
// with a caller-supplied ctx.
//
// Thread safety: serialized by f.mu with [File.Write], [File.ReadAt],
// and [File.WriteAt]. Use [File.Clone] for parallel I/O.
func (f *File) Read(p []byte) (int, error) {
	ctx, cancel := f.conn.opCtx(context.Background())
	defer cancel()
	return f.ReadCtx(ctx, p)
}

// WriteCtx is the ctx-taking variant of [File.Write]. Chunks p over
// multiple Twrites when len(p) exceeds the per-op clamp; the caller's
// ctx is used on every chunk, so a cancel mid-write returns a partial
// count with the ctx error per the [io.Writer] contract.
//
// Returns [io.ErrShortWrite] if the server reports a Twrite count less
// than the chunk size sent.
//
// Serializes against other I/O methods on the same *File via f.mu.
// For parallel I/O, use [File.Clone]. On ctx cancellation or deadline
// expiry, a Tflush(oldtag) is sent for the in-flight Twrite.
func (f *File) WriteCtx(ctx context.Context, p []byte) (int, error) {
	if err := f.beginOp(); err != nil {
		return 0, err
	}
	defer f.endOp()
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
		if m := f.maxChunk(); len(chunk) > int(m) {
			chunk = chunk[:m]
		}
		n, err := f.conn.twrite(ctx, f.fid, uint64(f.offset), chunk)
		if err != nil {
			return total, err
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

// Write writes len(p) bytes to the File starting at the current local
// offset, advancing the offset by bytes-written. Chunks the payload
// over multiple Twrites when len(p) exceeds min(iounit, msize -
// ioFrameOverhead).
//
// Returns [io.ErrShortWrite] if the server reports a Twrite count less
// than the chunk size sent -- per the [io.Writer] contract, a non-nil
// error must accompany any n < len(p) result.
//
// Context: Write derives its ctx from the Conn's [WithRequestTimeout]
// setting (default: infinite wait). The same ctx is used for every
// chunk. Callers needing per-op cancellation use [File.WriteCtx].
//
// Thread safety: serialized with other I/O methods on the same *File
// via f.mu.
func (f *File) Write(p []byte) (int, error) {
	ctx, cancel := f.conn.opCtx(context.Background())
	defer cancel()
	return f.WriteCtx(ctx, p)
}

// Seek sets the local offset for the next [File.Read] or [File.Write]
// on this File. Does NOT issue a wire op -- 9P is offset-addressed on
// every Tread/Twrite, so there is no server-side seek state to
// synchronize.
//
// Whence:
//   - [io.SeekStart]:   offset is the absolute position.
//   - [io.SeekCurrent]: offset is relative to the current position.
//   - [io.SeekEnd]:     offset is relative to the file's size, read
//     from f.cachedSize. cachedSize defaults to 0 and is populated by
//     [File.RefreshSize]; before it runs SeekEnd(0) returns 0 (correct for
//     an empty file) and SeekEnd(-n) for n > 0 returns a "negative
//     position" error with guidance to call File.RefreshSize first.
//
// Returns an error when the computed absolute position is negative.
// Seeking past the end of the file is allowed and does not error --
// subsequent Reads return [io.EOF], and subsequent Writes may extend
// the file (server-permitting).
//
// Seek on a directory fid succeeds (pure arithmetic). A subsequent
// [File.Read] on a directory fid surfaces the server's EISDIR (or
// equivalent) error; this matches [os.File] behavior.
//
// Thread safety: serialized with I/O methods via f.mu.
func (f *File) Seek(offset int64, whence int) (int64, error) {
	if err := f.beginOp(); err != nil {
		return 0, err
	}
	defer f.endOp()
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
			return f.offset, fmt.Errorf("client: negative position %d; SeekEnd requires File.RefreshSize to populate size", abs)
		}
		return f.offset, fmt.Errorf("client: negative position %d", abs)
	}
	f.offset = abs
	return abs, nil
}

// ReadAtCtx is the ctx-taking variant of [File.ReadAt]. Loops issuing
// Treads at off, off+n1, off+n1+n2, ... until p is filled or the
// server returns zero bytes ([io.EOF]). Each Tread uses the caller's
// ctx verbatim, so a cancel mid-chunk returns a partial count with
// the ctx error.
//
// Internally uses the zero-copy read path: each chunk of length <=
// maxChunk() is decoded directly from the wire into the corresponding
// sub-slice of p, skipping both the intermediate Rread.Data allocation
// AND the tread result-copy.
//
// Does NOT advance the local offset. Positional reads on the same File may
// run concurrently and are synchronized with Close by the lifecycle lock.
func (f *File) ReadAtCtx(ctx context.Context, p []byte, off int64) (int, error) {
	if err := f.beginOp(); err != nil {
		return 0, err
	}
	defer f.endOp()
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 {
		return 0, fmt.Errorf("client: ReadAt negative offset %d", off)
	}
	total := 0
	for total < len(p) {
		count := chunkLen(len(p)-total, f.maxChunk())
		// Zero-copy: dst aliases the caller's buffer at the chunk slot.
		// readAtZeroCopy decodes Rread.Data directly into dst[:count],
		// no intermediate copy.
		chunk := p[total : total+int(count)]
		n, err := f.conn.readAtZeroCopy(ctx, f.fid, uint64(off)+uint64(total), count, chunk)
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.EOF
		}
		total += n
	}
	return total, nil
}

// ReadAt reads len(p) bytes from the File starting at off. Satisfies
// the [io.ReaderAt] contract: returns a non-nil error whenever
// n < len(p), specifically [io.EOF] when the short return is at the
// end of file.
//
// ReadAt does NOT advance the local offset and may run concurrently with
// other positional I/O on the same File.
//
// Internally loops issuing Treads against the offset until either p
// is filled or the server returns zero bytes (EOF). Each Tread is
// clamped to min(iounit, msize - ioFrameOverhead).
//
// Context: ReadAt derives its ctx from the Conn's [WithRequestTimeout]
// setting (default: infinite wait). The same ctx is used for every
// chunk of the loop. Callers needing per-op cancellation use
// [File.ReadAtCtx].
func (f *File) ReadAt(p []byte, off int64) (int, error) {
	ctx, cancel := f.conn.opCtx(context.Background())
	defer cancel()
	return f.ReadAtCtx(ctx, p, off)
}

// ReadDir reads directory entries from this File, which must have been
// opened on a directory fid (see [Conn.OpenDir]).
//
// If n > 0, returns at most n entries; a subsequent call resumes at
// the next entry. If n <= 0, ReadDir loops issuing Treaddir round-
// trips until the server reports the directory is exhausted, and
// returns the full entry slice.
//
// When the directory is exhausted, ReadDir returns (nil-or-empty
// slice, nil). Callers emulating the [os.File.ReadDir] n>0 contract
// (which returns io.EOF on exhaustion) should check len(entries) == 0
// on nil err.
//
// Only supported on 9P2000.L Conns. On a .u Conn returns
// (nil, [ErrNotSupported]) -- .u directory enumeration uses a
// different wire op (Tread on a directory fid returning packed .u
// Stat entries) and is deferred to a future phase.
//
// The returned entries' [os.DirEntry.Info] method returns
// (nil, [ErrNotSupported]); Tgetattr is not wired through ReadDir.
// Callers needing an [fs.FileInfo] should walk to the entry and call
// [File.Stat].
//
// Like the non-ctx io methods, ReadDir honors the Conn's
// [WithRequestTimeout]; with no timeout configured it has no deadline.
//
// Thread safety: takes f.mu and mutates the internal
// readdirOffset cursor. Concurrent ReadDir on the same *File
// serializes. Use [File.Clone] for parallel enumeration if that is
// ever needed (rare -- directory enumeration is typically sequential).
func (f *File) ReadDir(n int) ([]os.DirEntry, error) {
	ctx, cancel := f.conn.opCtx(context.Background())
	defer cancel()
	return f.readDir(ctx, n)
}

// WriteAtCtx is the ctx-taking variant of [File.WriteAt]. Chunks p
// over multiple Twrites at off, off+n1, off+n1+n2, ...; each chunk
// uses the caller's ctx verbatim. Does NOT advance the local offset and may
// run concurrently with other positional I/O on the same File.
func (f *File) WriteAtCtx(ctx context.Context, p []byte, off int64) (int, error) {
	if err := f.beginOp(); err != nil {
		return 0, err
	}
	defer f.endOp()
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 {
		return 0, fmt.Errorf("client: WriteAt negative offset %d", off)
	}
	total := 0
	for total < len(p) {
		chunk := p[total:]
		if m := f.maxChunk(); len(chunk) > int(m) {
			chunk = chunk[:m]
		}
		n, err := f.conn.twrite(ctx, f.fid, uint64(off)+uint64(total), chunk)
		if err != nil {
			return total, err
		}
		total += int(n)
		if int(n) < len(chunk) {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}

// WriteAt writes len(p) bytes to the File starting at off. Satisfies
// the [io.WriterAt] contract: returns a non-nil error whenever
// n < len(p).
//
// WriteAt does NOT advance the local offset and may run concurrently with
// other positional I/O on the same File.
//
// Chunks the payload over multiple Twrites when len(p) exceeds
// min(iounit, msize - ioFrameOverhead). Returns [io.ErrShortWrite] if
// the server reports a Twrite count less than the chunk size sent.
//
// Context: WriteAt derives its ctx from the Conn's [WithRequestTimeout]
// setting (default: infinite wait). Callers needing per-op
// cancellation use [File.WriteAtCtx].
func (f *File) WriteAt(p []byte, off int64) (int, error) {
	ctx, cancel := f.conn.opCtx(context.Background())
	defer cancel()
	return f.WriteAtCtx(ctx, p, off)
}

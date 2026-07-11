package client

import (
	"context"
	"fmt"
	"io/fs"
	"os"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// readDir is the internal body of [File.ReadDir]. Takes ctx explicitly
// so a future ReadDirCtx variant can thread caller-supplied
// cancellation through without touching the public API.
//
// Loop structure:
//   - Issue Treaddir(fid, readdirOffset, maxChunk) until the server
//     returns zero bytes (directory exhausted) or we have n entries.
//   - Decode packed dirents from each Rreaddir.Data; update
//     readdirOffset to the final entry's Offset for the next call.
//   - Stop on first error; return whatever we accumulated so far.
//
// The dialect gate fires before any wire activity: .u uses a different
// directory-enumeration wire op (Tread on a directory fid returning
// packed Stat.u entries) which is out of scope here.
func (f *File) readDir(ctx context.Context, n int) ([]os.DirEntry, error) {
	if err := f.beginOp(); err != nil {
		return nil, err
	}
	defer f.endOp()
	if f.conn.dialect != protocolL {
		return nil, fmt.Errorf("%w: File.ReadDir requires 9P2000.L", ErrNotSupported)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	var entries []os.DirEntry
	for {
		if n > 0 && len(entries) >= n {
			return entries[:n], nil
		}
		reqOffset := f.readdirOffset
		count := f.maxChunk()
		req := &p9l.Treaddir{
			Fid:    f.fid,
			Offset: reqOffset,
			Count:  count,
		}
		resp, err := f.conn.roundTrip(ctx, req)
		if err != nil {
			return entries, err
		}
		if err := toError(resp); err != nil {
			return entries, err
		}
		r, ok := resp.(*p9l.Rreaddir)
		if !ok {
			putCachedRMsg(resp)
			return entries, fmt.Errorf("client: expected Rreaddir, got %v", resp.Type())
		}
		data := r.Data
		if len(data) == 0 {
			// Server indicates directory exhausted.
			putCachedRMsg(resp)
			return entries, nil
		}

		parsed, derr := proto.ParseDirents(data)
		// ParseDirents produces proto.Dirent values whose Name is an
		// owned string (string(bytes) copies). Safe to drop resp now.
		putCachedRMsg(resp)
		if derr != nil {
			return entries, derr
		}
		if len(parsed) == 0 {
			// Non-empty Data but no decodable entries -- defensive exit
			// rather than infinite loop. Treat as end-of-directory.
			return entries, nil
		}
		for _, d := range parsed {
			entries = append(entries, direntEntry{d: d})
			// Update the cursor to this entry's Offset BEFORE the n
			// check so a mid-parsed-batch early return leaves the next
			// call positioned exactly after the last yielded entry.
			// Treaddir Offset semantics: the server resumes AT the
			// entry whose Offset equals this value, so we pass the
			// per-entry offset forward verbatim.
			f.readdirOffset = d.Offset
			if n > 0 && len(entries) >= n {
				return entries[:n], nil
			}
		}
		// The cursor must strictly advance past the offset we requested.
		// A server returning a non-empty batch whose last entry repeats the
		// request offset would otherwise loop forever, appending duplicate
		// entries without bound (server cookies are monotonic, so a stuck
		// cursor is a protocol violation, not a legitimate response).
		if f.readdirOffset <= reqOffset {
			return entries, fmt.Errorf("client: readdir cursor did not advance past offset %d", reqOffset)
		}
	}
}

// direntEntry wraps a proto.Dirent so it satisfies [os.DirEntry]. Name
// and Type() are filled from the Dirent's server-provided fields;
// Info() returns [ErrNotSupported] since Tgetattr is not wired here.
type direntEntry struct {
	d proto.Dirent
}

// Compile-time assertion that direntEntry satisfies os.DirEntry.
var _ os.DirEntry = direntEntry{}

// Name returns the final path component of this entry. Never contains
// a slash -- the server sends leaf names only per the 9P protocol.
func (e direntEntry) Name() string { return e.d.Name }

// IsDir reports whether the entry is a directory, derived from the
// Linux DT_* type byte in the dirent. Linux DT_DIR == 4 per fs.h;
// verified via proto.DT_DIR.
func (e direntEntry) IsDir() bool {
	return e.d.Type == proto.DT_DIR
}

// Type returns an [fs.FileMode] carrying only the file-type bits that
// correspond to the Linux DT_* byte. Permission bits are zero;
// callers that need mode bits should combine Type() with a separate
// Stat call.
//
// DT_REG (regular file) and any unknown type byte map to 0, matching
// the [os.DirEntry] convention of zero for regular files.
func (e direntEntry) Type() fs.FileMode {
	switch e.d.Type {
	case proto.DT_DIR:
		return fs.ModeDir
	case proto.DT_LNK:
		return fs.ModeSymlink
	case proto.DT_BLK:
		return fs.ModeDevice
	case proto.DT_CHR:
		return fs.ModeDevice | fs.ModeCharDevice
	case proto.DT_FIFO:
		return fs.ModeNamedPipe
	case proto.DT_SOCK:
		return fs.ModeSocket
	default:
		return 0 // DT_REG or unknown -- regular file
	}
}

// Info returns [fs.FileInfo] for the entry. Not supported; callers
// that need FileInfo should walk to the entry and use [File.Stat].
func (e direntEntry) Info() (fs.FileInfo, error) {
	return nil, ErrNotSupported
}

package client

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io/fs"
	"os"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// readDir is the internal body of [File.ReadDir]. Takes ctx explicitly
// so a future Phase 22 ReadDirCtx variant can thread caller-supplied
// cancellation through without touching the public API.
//
// Loop structure:
//   - Issue Treaddir(fid, readdirOffset, maxChunk) until the server
//     returns zero bytes (directory exhausted) or we have n entries.
//   - Decode packed dirents from each Rreaddir.Data; update
//     readdirOffset to the final entry's Offset for the next call.
//   - Stop on first error; return whatever we accumulated so far.
//
// The dialect gate fires before any wire activity per Q4 resolution:
// .u uses a different directory-enumeration wire op (Tread on a
// directory fid returning packed Stat.u entries) which is out of
// scope for Phase 20.
func (f *File) readDir(ctx context.Context, n int) ([]os.DirEntry, error) {
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
		count := f.maxChunk()
		req := &p9l.Treaddir{
			Fid:    f.fid,
			Offset: f.readdirOffset,
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

		parsed, _, derr := parseDirents(data)
		// parseDirents produces proto.Dirent values whose Name is an
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
	}
}

// parseDirents decodes a packed Rreaddir.Data byte slice into a slice
// of proto.Dirent values plus the Offset of the last decoded entry
// (which becomes the next Treaddir's Offset cursor).
//
// Packed layout per 9P2000.L (inverse of server/dirent.go EncodeDirents):
//
//	QID[13]       = type[1] + version[4] + path[8]
//	Offset[8]     = little-endian uint64 (server-provided cursor)
//	Type[1]       = Linux DT_* dirent type byte
//	Name[s]       = len[2] + bytes[len]    (little-endian uint16 length prefix)
//
// Bounds-checked at every field extraction -- T-20-04 mitigation.
// Returns the partial slice on decode error so the caller can surface
// whatever entries arrived before the corruption.
func parseDirents(data []byte) ([]proto.Dirent, uint64, error) {
	const minEntrySize = 13 + 8 + 1 + 2 // QID + Offset + Type + NameLen
	out := make([]proto.Dirent, 0, 8)
	br := bytes.NewReader(data)
	var lastOffset uint64
	for br.Len() > 0 {
		if br.Len() < minEntrySize {
			return out, lastOffset, fmt.Errorf("client: truncated dirent header (%d bytes left, need %d)", br.Len(), minEntrySize)
		}
		var qid proto.QID
		var typeBuf [1]byte
		if _, err := br.Read(typeBuf[:]); err != nil {
			return out, lastOffset, fmt.Errorf("client: dirent qid type: %w", err)
		}
		qid.Type = proto.QIDType(typeBuf[0])

		var verBuf [4]byte
		if _, err := br.Read(verBuf[:]); err != nil {
			return out, lastOffset, fmt.Errorf("client: dirent qid version: %w", err)
		}
		qid.Version = binary.LittleEndian.Uint32(verBuf[:])

		var pathBuf [8]byte
		if _, err := br.Read(pathBuf[:]); err != nil {
			return out, lastOffset, fmt.Errorf("client: dirent qid path: %w", err)
		}
		qid.Path = binary.LittleEndian.Uint64(pathBuf[:])

		var offBuf [8]byte
		if _, err := br.Read(offBuf[:]); err != nil {
			return out, lastOffset, fmt.Errorf("client: dirent offset: %w", err)
		}
		entryOffset := binary.LittleEndian.Uint64(offBuf[:])

		var dtypeBuf [1]byte
		if _, err := br.Read(dtypeBuf[:]); err != nil {
			return out, lastOffset, fmt.Errorf("client: dirent type byte: %w", err)
		}
		entryType := dtypeBuf[0]

		var nameLenBuf [2]byte
		if _, err := br.Read(nameLenBuf[:]); err != nil {
			return out, lastOffset, fmt.Errorf("client: dirent name len: %w", err)
		}
		nameLen := binary.LittleEndian.Uint16(nameLenBuf[:])
		if int(nameLen) > br.Len() {
			return out, lastOffset, fmt.Errorf("client: dirent name len %d exceeds remaining %d bytes", nameLen, br.Len())
		}
		nameBuf := make([]byte, nameLen)
		if _, err := br.Read(nameBuf); err != nil {
			return out, lastOffset, fmt.Errorf("client: dirent name: %w", err)
		}

		out = append(out, proto.Dirent{
			QID:    qid,
			Offset: entryOffset,
			Type:   entryType,
			Name:   string(nameBuf),
		})
		lastOffset = entryOffset
	}
	return out, lastOffset, nil
}

// direntEntry wraps a proto.Dirent so it satisfies [os.DirEntry]. Name
// and Type() are filled from the Dirent's server-provided fields;
// Info() returns [ErrNotSupported] until Phase 21 wires Tgetattr.
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
// correspond to the Linux DT_* byte. Permission bits are zero until
// Phase 21 ships Tgetattr; callers that need mode bits should combine
// Type() with a separate Stat call in a future phase.
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

// Info returns [fs.FileInfo] for the entry. Not supported in Phase 20
// -- Phase 21 wires Tgetattr which will populate size, mode, and
// timestamps. Callers that need FileInfo before Phase 21 must walk to
// the entry and use a future [File.Stat] helper.
func (e direntEntry) Info() (fs.FileInfo, error) {
	return nil, ErrNotSupported
}

package proto

import (
	"encoding/binary"
	"fmt"

	"github.com/dotwaffle/ninep/internal/bufpool"
)

// Dirent wire layout, packed back to back in Rreaddir data:
//
//	qid[13] + offset[8] + type[1] + name[s]
//
// where name[s] = len[2] + name_bytes. Both directions of the codec live
// here, next to the Dirent type, so the server encoder and client parser
// cannot drift apart.

// EncodeDirents packs dirents into bytes fitting within maxBytes.
// Returns the packed bytes and the number of entries that fit.
//
// The returned []byte is a freshly-allocated copy-out -- safe to retain past
// the call boundary.
func EncodeDirents(dirents []Dirent, maxBytes uint32) ([]byte, int) {
	if len(dirents) == 0 {
		return nil, 0
	}

	bufPtr := bufpool.GetMsgBuf(int(maxBytes))
	defer bufpool.PutMsgBuf(bufPtr)

	n, count := EncodeDirentsInto((*bufPtr)[:maxBytes], dirents)

	// Copy-out -- the pooled buffer returns to the pool via defer AFTER
	// this function returns, at which point the caller holds only the
	// fresh `out` slice. No aliasing; safe even though the response
	// encoder runs later than this PutMsgBuf.
	out := make([]byte, n)
	copy(out, (*bufPtr)[:n])
	return out, count
}

// EncodeDirentsInto packs dirents into dst. It returns the number of bytes
// written and the number of entries that fit.
//
// It is the zero-allocation backend for EncodeDirents.
func EncodeDirentsInto(dst []byte, dirents []Dirent) (int, int) {
	off := 0
	count := 0
	for _, d := range dirents {
		// The name length is a 2-byte wire field. A name that does not fit
		// would wrap on PutUint16 while the body still advances by the full
		// length, desyncing the dirent stream. Skip such entries (names are
		// NAME_MAX-bounded in practice, so this is defensive) rather than
		// emitting a corrupt record.
		if len(d.Name) > 0xFFFF {
			continue
		}

		entrySize := QIDSize + 8 + 1 + 2 + len(d.Name)
		if off+entrySize > len(dst) {
			break
		}

		// QID: type[1] + version[4] + path[8]
		dst[off] = uint8(d.QID.Type)
		binary.LittleEndian.PutUint32(dst[off+1:], d.QID.Version)
		binary.LittleEndian.PutUint64(dst[off+5:], d.QID.Path)
		off += QIDSize

		// Offset[8]
		binary.LittleEndian.PutUint64(dst[off:], d.Offset)
		off += 8

		// Type[1]
		dst[off] = d.Type
		off++

		// Name: len[2] + data[len]
		binary.LittleEndian.PutUint16(dst[off:], uint16(len(d.Name)))
		off += 2
		copy(dst[off:], d.Name)
		off += len(d.Name)

		count++
	}
	return off, count
}

// ParseDirents decodes packed Rreaddir data into Dirent values. Every field
// extraction is bounds-checked; on a truncated or corrupt stream it returns
// the entries decoded so far alongside the error, so callers can surface
// whatever arrived before the corruption. Each Name is an owned copy --
// data may be reused or released after the call.
func ParseDirents(data []byte) ([]Dirent, error) {
	const minEntrySize = QIDSize + 8 + 1 + 2 // QID + Offset + Type + NameLen
	out := make([]Dirent, 0, 8)
	for off := 0; off < len(data); {
		rest := len(data) - off
		if rest < minEntrySize {
			return out, fmt.Errorf("truncated dirent header (%d bytes left, need %d)", rest, minEntrySize)
		}
		var d Dirent
		d.QID.Type = QIDType(data[off])
		d.QID.Version = binary.LittleEndian.Uint32(data[off+1:])
		d.QID.Path = binary.LittleEndian.Uint64(data[off+5:])
		d.Offset = binary.LittleEndian.Uint64(data[off+QIDSize:])
		d.Type = data[off+QIDSize+8]
		nameLen := int(binary.LittleEndian.Uint16(data[off+QIDSize+9:]))
		off += minEntrySize
		if nameLen > len(data)-off {
			return out, fmt.Errorf("dirent name len %d exceeds remaining %d bytes", nameLen, len(data)-off)
		}
		d.Name = string(data[off : off+nameLen])
		off += nameLen
		out = append(out, d)
	}
	return out, nil
}

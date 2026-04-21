package server

import (
	"encoding/binary"

	"github.com/dotwaffle/ninep/internal/bufpool"
	"github.com/dotwaffle/ninep/proto"
)

// EncodeDirents packs dirents into bytes fitting within maxBytes.
// Returns the packed bytes and the number of entries that fit.
//
// Each entry is encoded as:
//
//	qid[13] + offset[8] + type[1] + name[s]
//
// where name[s] = len[2] + name_bytes.
//
// The returned []byte is a freshly-allocated copy-out — safe to retain past
// the call boundary.
func EncodeDirents(dirents []proto.Dirent, maxBytes uint32) ([]byte, int) {
	if len(dirents) == 0 {
		return nil, 0
	}

	bufPtr := bufpool.GetMsgBuf(int(maxBytes))
	defer bufpool.PutMsgBuf(bufPtr)

	n, count := EncodeDirentsInto((*bufPtr)[:maxBytes], dirents)

	// Copy-out — the pooled buffer returns to the pool via defer AFTER
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
// It is the zero-allocation backend for EncodeDirents and readdirSimple.
func EncodeDirentsInto(dst []byte, dirents []proto.Dirent) (int, int) {
	off := 0
	count := 0
	for _, d := range dirents {
		entrySize := proto.QIDSize + 8 + 1 + 2 + len(d.Name)
		if off+entrySize > len(dst) {
			break
		}

		// QID: type[1] + version[4] + path[8]
		dst[off] = uint8(d.QID.Type)
		binary.LittleEndian.PutUint32(dst[off+1:], d.QID.Version)
		binary.LittleEndian.PutUint64(dst[off+5:], d.QID.Path)
		off += 13

		// Offset[8]
		binary.LittleEndian.PutUint64(dst[off:], d.Offset)
		off += 8

		// Type[1]
		dst[off] = d.Type
		off += 1

		// Name: len[2] + data[len]
		binary.LittleEndian.PutUint16(dst[off:], uint16(len(d.Name)))
		off += 2
		copy(dst[off:], d.Name)
		off += len(d.Name)

		count++
	}
	return off, count
}

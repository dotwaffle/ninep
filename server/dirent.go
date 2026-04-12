package server

import (
	"bytes"

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
func EncodeDirents(dirents []proto.Dirent, maxBytes uint32) ([]byte, int) {
	if len(dirents) == 0 {
		return nil, 0
	}

	buf := &bytes.Buffer{}
	count := 0

	for _, d := range dirents {
		entrySize := proto.QIDSize + 8 + 1 + 2 + len(d.Name)
		if buf.Len()+entrySize > int(maxBytes) {
			break
		}

		// All proto.Write* functions write to bytes.Buffer, which
		// never returns write errors.
		_ = proto.WriteQID(buf, d.QID)
		_ = proto.WriteUint64(buf, d.Offset)
		_ = proto.WriteUint8(buf, d.Type)
		_ = proto.WriteString(buf, d.Name)
		count++
	}

	return buf.Bytes(), count
}

//go:build freebsd

package passthrough

import (
	"encoding/binary"

	"golang.org/x/sys/unix"

	"github.com/dotwaffle/ninep/proto"
)

// mknodat is the FreeBSD shim for the shared Mknod (dir.go): the raw
// syscall's dev argument is uint64 here, int on Linux.
func mknodat(dirfd int, name string, mode uint32, dev uint64) error {
	return unix.Mknodat(dirfd, name, mode, dev)
}

// parseDirents parses raw FreeBSD getdirentries output into proto.Dirent
// entries. Skips "." and "..".
//
// FreeBSD struct dirent (24-byte header before name):
//
//	Fileno uint64    // bytes 0..7
//	Off    int64     // bytes 8..15
//	Reclen uint16    // bytes 16..17
//	Type   uint8     // byte 18
//	Pad0   uint8     // byte 19
//	Namlen uint16    // bytes 20..21
//	Pad1   uint16    // bytes 22..23
//	Name   variable  // byte 24..
//
// Verified against golang.org/x/sys/unix ztypes_freebsd_amd64.go.
func parseDirents(buf []byte) []proto.Dirent {
	const headerLen = 24
	var dirents []proto.Dirent

	for len(buf) >= headerLen {
		ino := binary.LittleEndian.Uint64(buf[0:8])
		offset := binary.LittleEndian.Uint64(buf[8:16])
		reclen := binary.LittleEndian.Uint16(buf[16:18])
		dtype := buf[18]
		namlen := binary.LittleEndian.Uint16(buf[20:22])

		if int(reclen) > len(buf) || reclen < headerLen {
			break
		}
		if int(namlen) > int(reclen)-headerLen {
			break
		}

		name := string(buf[headerLen : headerLen+int(namlen)])
		if name != "." && name != ".." {
			dirents = append(dirents, proto.Dirent{
				QID: proto.QID{
					Type: dtypeToQIDType(dtype),
					Path: ino,
				},
				Offset: offset,
				Type:   dtype,
				Name:   name,
			})
		}
		buf = buf[reclen:]
	}

	return dirents
}

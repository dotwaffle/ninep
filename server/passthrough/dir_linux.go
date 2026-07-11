//go:build linux

package passthrough

import (
	"bytes"
	"encoding/binary"

	"golang.org/x/sys/unix"

	"github.com/dotwaffle/ninep/proto"
)

// mknodat is the Linux shim for the shared Mknod (dir.go): the raw
// syscall's dev argument is int here, uint64 on FreeBSD.
func mknodat(dirfd int, name string, mode uint32, dev uint64) error {
	return unix.Mknodat(dirfd, name, mode, int(dev))
}

// parseDirents parses raw getdents64 output into proto.Dirent entries.
// Skips "." and ".." entries.
//
// linux_dirent64 is laid out as: d_ino[8] d_off[8] d_reclen[2] d_type[1] d_name[...].
// encoding/binary handles alignment -- Linux getdents64 buffers guarantee
// little-endian but not struct alignment, so binary.LittleEndian.Uint*
// reads directly from the []byte slice (shift-and-OR) with no alignment
// requirement on the source.
func parseDirents(buf []byte) []proto.Dirent {
	var dirents []proto.Dirent

	for len(buf) > 0 {
		// Minimum fixed-header size: d_ino[8] + d_off[8] + d_reclen[2] + d_type[1] = 19.
		if len(buf) < 19 {
			break
		}

		ino := binary.LittleEndian.Uint64(buf[0:8])
		offset := binary.LittleEndian.Uint64(buf[8:16])
		reclen := binary.LittleEndian.Uint16(buf[16:18])
		dtype := buf[18]

		if int(reclen) > len(buf) || reclen < 19 {
			break
		}

		// Name is null-terminated starting at offset 19.
		nameBytes := buf[19:reclen]
		before, _, ok := bytes.Cut(nameBytes, []byte{0})
		var name string
		if ok {
			name = string(before)
		} else {
			name = string(nameBytes)
		}

		// Skip . and ..
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

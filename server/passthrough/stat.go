package passthrough

import (
	"errors"
	"syscall"

	"github.com/dotwaffle/ninep/proto"
)

// statToAttr converts a syscall.Stat_t to proto.Attr with all fields mapped.
// UID/GID are transformed through mapper.FromHost for protocol-level reporting.
func statToAttr(st *syscall.Stat_t, mapper UIDMapper) proto.Attr {
	uid, gid := mapper.FromHost(st.Uid, st.Gid)
	return proto.Attr{
		Valid:     proto.AttrAll,
		QID:      statToQID(st),
		Mode:     st.Mode,
		UID:      uid,
		GID:      gid,
		NLink:    st.Nlink,
		RDev:     st.Rdev,
		Size:     uint64(st.Size),
		BlkSize:  uint64(st.Blksize),
		Blocks:   uint64(st.Blocks),
		ATimeSec: uint64(st.Atim.Sec),
		ATimeNSec: uint64(st.Atim.Nsec),
		MTimeSec: uint64(st.Mtim.Sec),
		MTimeNSec: uint64(st.Mtim.Nsec),
		CTimeSec: uint64(st.Ctim.Sec),
		CTimeNSec: uint64(st.Ctim.Nsec),
	}
}

// statToQID extracts a QID from a syscall.Stat_t. The type is derived from
// the file mode, the version from ctime seconds, and the path from the inode
// number.
func statToQID(st *syscall.Stat_t) proto.QID {
	var t proto.QIDType
	switch st.Mode & syscall.S_IFMT {
	case syscall.S_IFDIR:
		t = proto.QTDIR
	case syscall.S_IFLNK:
		t = proto.QTSYMLINK
	default:
		t = proto.QTFILE
	}
	return proto.QID{
		Type:    t,
		Version: uint32(st.Ctim.Sec),
		Path:    st.Ino,
	}
}

// toProtoErr converts an OS error to a proto.Errno. If the error contains a
// syscall.Errno, the numeric value is used directly as a proto.Errno (Linux
// errno values match the 9P2000.L wire format). Unknown errors map to EIO.
// Returns nil for nil input.
func toProtoErr(err error) error {
	if err == nil {
		return nil
	}
	if errno, ok := errors.AsType[syscall.Errno](err); ok {
		return proto.Errno(errno)
	}
	return proto.EIO
}

// direntType converts a file mode to the DT_* type value used in readdir
// responses. The type is extracted by shifting the S_IFMT bits.
func direntType(mode uint32) uint8 {
	return uint8((mode & syscall.S_IFMT) >> 12)
}

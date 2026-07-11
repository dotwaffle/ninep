//go:build freebsd

package passthrough

import (
	"errors"

	"golang.org/x/sys/unix"

	"github.com/dotwaffle/ninep/proto"
)

// statToAttr converts a unix.Stat_t to proto.Attr with all fields mapped.
// UID/GID are transformed through mapper.FromHost for protocol-level reporting.
//
// FreeBSD's Stat_t.Mode is uint16 (vs Linux's uint32) and Blksize is int32
// (vs Linux's int64); explicit casts widen to the proto.Attr field types.
func statToAttr(st *unix.Stat_t, root *Root) proto.Attr {
	uid, gid := root.mapper.FromHost(st.Uid, st.Gid)
	return proto.Attr{
		Valid:     proto.AttrAll,
		QID:       root.qidFor(st),
		Mode:      uint32(st.Mode),
		UID:       uid,
		GID:       gid,
		NLink:     st.Nlink,
		RDev:      st.Rdev,
		Size:      uint64(st.Size),
		BlkSize:   uint64(st.Blksize),
		Blocks:    uint64(st.Blocks),
		ATimeSec:  uint64(st.Atim.Sec),
		ATimeNSec: uint64(st.Atim.Nsec),
		MTimeSec:  uint64(st.Mtim.Sec),
		MTimeNSec: uint64(st.Mtim.Nsec),
		CTimeSec:  uint64(st.Ctim.Sec),
		CTimeNSec: uint64(st.Ctim.Nsec),
	}
}

// statToQID extracts the platform fields of a QID. Root.qidFor replaces Path
// with a root-scoped device/inode identity before exposing it to clients.
func statToQID(st *unix.Stat_t) proto.QID {
	var t proto.QIDType
	switch uint32(st.Mode) & unix.S_IFMT {
	case unix.S_IFDIR:
		t = proto.QTDIR
	case unix.S_IFLNK:
		t = proto.QTSYMLINK
	default:
		t = proto.QTFILE
	}
	return proto.QID{
		Type:    t,
		Version: qidVersion(st.Ctim.Sec, st.Ctim.Nsec),
		Path:    st.Ino,
	}
}

// toProtoErr converts an OS error to a proto.Errno via proto.ErrnoFromUnix
// (which translates FreeBSD errno values to their Linux wire equivalents).
// Returns nil for nil input.
func toProtoErr(err error) error {
	if err == nil {
		return nil
	}
	if errno, ok := errors.AsType[unix.Errno](err); ok {
		return proto.ErrnoFromUnix(errno)
	}
	return proto.EIO
}

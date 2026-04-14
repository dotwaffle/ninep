package proto

import "math"

// Protocol-level constants for 9P message framing and limits.
const (
	// HeaderSize is the fixed header size in bytes: size[4] + type[1] + tag[2].
	HeaderSize uint32 = 7

	// MaxWalkElements is the maximum number of path elements in a single
	// Twalk/Rwalk message, as defined by the 9P specification.
	MaxWalkElements = 16

	// MaxStringLen is the maximum length of a 9P string in bytes. Strings
	// are length-prefixed with a uint16, so the maximum is 65535.
	MaxStringLen = math.MaxUint16

	// QIDSize is the wire size of a QID in bytes: type[1] + version[4] + path[8].
	QIDSize = 13
)

// Sentinel fid and tag values.
var (
	// NoTag is the tag value used for Tversion/Rversion messages, which
	// are not tagged.
	NoTag Tag = Tag(math.MaxUint16)

	// NoFid is the fid value indicating "no fid", used for example as the
	// afid in Tattach when no authentication is required.
	NoFid Fid = Fid(math.MaxUint32)
)

// Dirent type constants for the Type field of Dirent. These match Linux's
// DT_* values from <dirent.h> and the linux_dirent64 d_type byte returned
// by getdents64(2). The 9P2000.L kernel client passes this byte verbatim to
// dir_emit() in v9fs_dir_readdir_dotl, so servers MUST use these values
// (not 9P QID type bits) for filesystem clients to work correctly.
//
// Values verified against /usr/include/dirent.h (glibc) and
// golang.org/x/sys/unix zerrors_linux.go.
const (
	DT_UNKNOWN uint8 = 0  // Unknown file type.
	DT_FIFO    uint8 = 1  // Named pipe (FIFO).
	DT_CHR     uint8 = 2  // Character device.
	DT_DIR     uint8 = 4  // Directory.
	DT_BLK     uint8 = 6  // Block device.
	DT_REG     uint8 = 8  // Regular file.
	DT_LNK     uint8 = 10 // Symbolic link.
	DT_SOCK    uint8 = 12 // Unix-domain socket.
)

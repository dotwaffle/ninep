package proto

// MaxDataSize is the hard upper bound on data allocations from untrusted wire
// input (e.g. Rread, Twrite, Rreaddir count fields). It is intentionally
// larger than the typical negotiated msize (4 MiB) to avoid rejecting valid
// messages, but small enough to prevent a crafted uint32 count from triggering
// a multi-gigabyte allocation and OOM.
const MaxDataSize = 1 << 24 // 16 MiB

// Fid is a 32-bit handle identifying a file on the server, scoped to a
// single 9P connection.
type Fid uint32

// Tag is a 16-bit identifier used to match requests with responses. Each
// outstanding request on a connection must use a unique tag.
type Tag uint16

// QIDType classifies the file a QID refers to.
type QIDType uint8

// QID type constants.
const (
	QTDIR     QIDType = 0x80 // Directory.
	QTAPPEND  QIDType = 0x40 // Append-only file.
	QTEXCL    QIDType = 0x20 // Exclusive-use file.
	QTMOUNT   QIDType = 0x10 // Mounted channel.
	QTAUTH    QIDType = 0x08 // Authentication file.
	QTTMP     QIDType = 0x04 // Temporary file.
	QTSYMLINK QIDType = 0x02 // Symbolic link (9P2000.u / 9P2000.L).
	QTFILE    QIDType = 0x00 // Regular file.
)

// QID is the server's unique identification for a file. It consists of a
// type byte, a version number (incremented on changes), and a 64-bit path
// that uniquely identifies the file on the server.
type QID struct {
	Type    QIDType
	Version uint32
	Path    uint64
}

// FileMode represents 9P file permission and type bits.
type FileMode uint32

// 9P file mode constants.
const (
	DMDIR    FileMode = 0x80000000 // Directory.
	DMAPPEND FileMode = 0x40000000 // Append-only.
	DMEXCL   FileMode = 0x20000000 // Exclusive-use.
	DMTMP    FileMode = 0x04000000 // Temporary.
)

// AttrMask is a bitmask selecting which attributes to retrieve in a
// Tgetattr request.
type AttrMask uint64

// P9_GETATTR_* bitmask constants for Tgetattr request_mask.
const (
	AttrMode        AttrMask = 0x00000001
	AttrNLink       AttrMask = 0x00000002
	AttrUID         AttrMask = 0x00000004
	AttrGID         AttrMask = 0x00000008
	AttrRDev        AttrMask = 0x00000010
	AttrATime       AttrMask = 0x00000020
	AttrMTime       AttrMask = 0x00000040
	AttrCTime       AttrMask = 0x00000080
	AttrINo         AttrMask = 0x00000100
	AttrSize        AttrMask = 0x00000200
	AttrBlocks      AttrMask = 0x00000400
	AttrBTime       AttrMask = 0x00000800
	AttrGen         AttrMask = 0x00001000
	AttrDataVersion AttrMask = 0x00002000
	AttrBasic       AttrMask = 0x000007ff // Mode through Blocks.
	AttrAll         AttrMask = 0x00003fff // All defined attributes.
)

// SetAttrMask is a bitmask selecting which attributes to set in a
// Tsetattr request.
type SetAttrMask uint32

// P9_SETATTR_* bitmask constants for Tsetattr valid field.
const (
	SetAttrMode  SetAttrMask = 0x00000001
	SetAttrUID   SetAttrMask = 0x00000002
	SetAttrGID   SetAttrMask = 0x00000004
	SetAttrSize  SetAttrMask = 0x00000008
	SetAttrATime SetAttrMask = 0x00000010
	SetAttrMTime SetAttrMask = 0x00000020
	SetAttrCTime SetAttrMask = 0x00000040 // Server-managed, client hint.
)

// Attr holds the file attributes returned by Rgetattr.
type Attr struct {
	Valid       AttrMask
	QID         QID
	Mode        uint32
	UID         uint32
	GID         uint32
	NLink       uint64
	RDev        uint64
	Size        uint64
	BlkSize     uint64
	Blocks      uint64
	ATimeSec    uint64
	ATimeNSec   uint64
	MTimeSec    uint64
	MTimeNSec   uint64
	CTimeSec    uint64
	CTimeNSec   uint64
	BTimeSec    uint64
	BTimeNSec   uint64
	Gen         uint64
	DataVersion uint64
}

// SetAttr holds the file attributes to set in a Tsetattr request.
type SetAttr struct {
	Valid     SetAttrMask
	Mode      uint32
	UID       uint32
	GID       uint32
	Size      uint64
	ATimeSec  uint64
	ATimeNSec uint64
	MTimeSec  uint64
	MTimeNSec uint64
}

// Dirent represents a single directory entry as returned by Rreaddir.
type Dirent struct {
	QID    QID
	Offset uint64
	Type   uint8
	Name   string
}

// FSStat holds filesystem statistics returned by Rstatfs.
type FSStat struct {
	Type    uint32
	BSize   uint32
	Blocks  uint64
	BFree   uint64
	BAvail  uint64
	Files   uint64
	FFree   uint64
	FSID    uint64
	NameLen uint32
}

// LockType identifies the lock type for Tlock/Tgetlock operations.
type LockType uint8

// Lock type constants.
const (
	LockTypeRdLck LockType = 0
	LockTypeWrLck LockType = 1
	LockTypeUnlck LockType = 2
)

// LockFlags contains flags for Tlock operations.
type LockFlags uint32

// Lock flag constants.
const (
	LockFlagBlock   LockFlags = 1
	LockFlagReclaim LockFlags = 2
)

// LockStatus is the response status for Rlock.
type LockStatus uint8

// Lock status constants.
const (
	LockStatusOK      LockStatus = 0
	LockStatusBlocked LockStatus = 1
	LockStatusError   LockStatus = 2
	LockStatusGrace   LockStatus = 3
)

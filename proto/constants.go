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

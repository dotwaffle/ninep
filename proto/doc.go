// Package proto defines the shared types, constants, and encoding helpers for
// the 9P2000.L and 9P2000.u wire protocols.
//
// # Wire Format
//
// All 9P messages use little-endian byte order. Each message on the wire is
// framed with a 4-byte size prefix (total message length including the prefix
// itself), followed by a 1-byte type and 2-byte tag, then the message body.
// The fixed header size is defined by [HeaderSize] (7 bytes).
//
// # Message Interface
//
// Every 9P message type implements the [Message] interface, which provides
// [Message.Type] (the wire type byte), [Message.EncodeTo], and
// [Message.DecodeFrom]. These methods handle the message body only; the
// size/type/tag header is managed by the codec layer in the p9l and p9u
// sub-packages.
//
// # Key Types
//
//   - [MessageType] -- identifies each 9P message on the wire (Tversion,
//     Rversion, Twalk, Rwalk, etc.)
//   - [QID] -- server-unique file identifier: type byte, version, 64-bit path
//   - [Attr] / [AttrMask] -- file attributes and selection mask for Tgetattr
//   - [SetAttr] / [SetAttrMask] -- attribute modification for Tsetattr
//   - [FileMode] -- 9P file permission and type bits
//   - [Dirent] -- directory entry for Rreaddir
//   - [FSStat] -- filesystem statistics for Rstatfs
//   - [Fid] -- 32-bit handle identifying a file on a connection
//   - [Tag] -- 16-bit request/response correlation identifier
//   - [Errno] -- 9P error numbers mapped to protocol error responses
//
// # Encoding Helpers
//
// Low-level encoding and decoding functions ([WriteUint32], [ReadUint32],
// [WriteString], [ReadString], etc.) use [encoding/binary.LittleEndian]
// directly for zero-allocation wire encoding on hot paths.
//
// # Protocol-Specific Codecs
//
// Protocol-specific message types and top-level Encode/Decode functions live
// in sub-packages:
//
//   - [github.com/dotwaffle/ninep/proto/p9l] -- 9P2000.L codec (Linux v9fs)
//   - [github.com/dotwaffle/ninep/proto/p9u] -- 9P2000.u codec (Unix extensions)
package proto

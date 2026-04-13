// Package p9u implements the 9P2000.u wire codec.
//
// 9P2000.u is the Unix extension to the base 9P2000 protocol, adding numeric
// UID/GID fields, error strings with errno values, and extended stat
// structures. It is used by some Plan 9 derivatives and user-space 9P
// implementations.
//
// # Codec Functions
//
// [Encode] writes a complete 9P2000.u message to an [io.Writer], including
// the size[4] + type[1] + tag[2] header followed by the message body.
//
// [Decode] reads a complete 9P2000.u message from an [io.Reader], parsing the
// header and returning the decoded [proto.Message] along with its tag.
//
// # Message Types
//
// This package defines all T-message and R-message structs for the 9P2000.u
// protocol (e.g., Tauth/Rauth, Tattach/Rattach, Topen/Ropen, Tread/Rread).
// Each implements the [proto.Message] interface.
package p9u

// Package p9l implements the 9P2000.L wire codec.
//
// 9P2000.L is the primary 9P protocol variant used by the Linux kernel's v9fs
// client. It extends the base 9P2000 protocol with Linux-specific operations
// such as getattr/setattr with POSIX attribute masks, readdir with dirent
// encoding, symlink, mknod, lock, xattr, and statfs.
//
// # Codec Functions
//
// [Encode] writes a complete 9P2000.L message to an [io.Writer], including
// the size[4] + type[1] + tag[2] header followed by the message body.
//
// [Decode] reads a complete 9P2000.L message from an [io.Reader], parsing the
// header and returning the decoded [proto.Message] along with its tag.
//
// # Message Types
//
// This package defines all T-message and R-message structs for the 9P2000.L
// protocol (e.g., Tlopen/Rlopen, Tgetattr/Rgetattr, Treaddir/Rreaddir).
// Each implements the [proto.Message] interface.
package p9l

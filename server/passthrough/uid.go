// Package passthrough implements a 9P filesystem that proxies all operations
// to the host OS filesystem. It validates the entire ninep library API surface
// and serves as a production-grade reference implementation.
//
// All file operations use *at syscalls relative to directory file descriptors,
// preventing path traversal attacks. UID/GID mapping is configurable with
// identity mapping as the default.
//
// The server process needs appropriate OS permissions for non-identity UID/GID
// mapping (typically CAP_CHOWN and CAP_FOWNER capabilities on Linux).
package passthrough

// UIDMapper provides bidirectional UID/GID mapping between the 9P protocol
// and the host OS. ToHost maps protocol UIDs to OS UIDs (used for operations
// like Setattr). FromHost maps OS UIDs to protocol UIDs (used for operations
// like Getattr).
type UIDMapper struct {
	// ToHost maps 9P UIDs to host OS UIDs.
	ToHost func(uid, gid uint32) (uint32, uint32)
	// FromHost maps host OS UIDs to 9P UIDs.
	FromHost func(uid, gid uint32) (uint32, uint32)
}

// IdentityMapper returns a UIDMapper where both ToHost and FromHost return
// uid/gid unchanged. This is the default mapper.
func IdentityMapper() UIDMapper {
	id := func(uid, gid uint32) (uint32, uint32) { return uid, gid }
	return UIDMapper{ToHost: id, FromHost: id}
}

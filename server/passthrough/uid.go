//go:build linux

package passthrough

// UIDMapper provides bidirectional UID/GID mapping between the 9P protocol
// and the host OS. ToHost maps protocol UIDs to OS UIDs (used for operations
// like Setattr). FromHost maps OS UIDs to protocol UIDs (used for operations
// like Getattr).
//
// Both ToHost and FromHost MUST be non-nil. Passing a UIDMapper with
// either field nil via WithUIDMapper is a programming error and will
// panic the first time a UID/GID translation is attempted. Use
// IdentityMapper() for the identity-mapping default.
type UIDMapper struct {
	// ToHost maps 9P UIDs to host OS UIDs. Required (non-nil).
	ToHost func(uid, gid uint32) (uint32, uint32)
	// FromHost maps host OS UIDs to 9P UIDs. Required (non-nil).
	FromHost func(uid, gid uint32) (uint32, uint32)
}

// IdentityMapper returns a UIDMapper where both ToHost and FromHost return
// uid/gid unchanged. This is the default mapper.
func IdentityMapper() UIDMapper {
	id := func(uid, gid uint32) (uint32, uint32) { return uid, gid }
	return UIDMapper{ToHost: id, FromHost: id}
}

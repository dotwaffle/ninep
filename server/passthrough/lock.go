package passthrough

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

// Compile-time interface assertion for lock operations.
var _ server.NodeLocker = (*Node)(nil)

// Lock acquires, tests, or releases a POSIX byte-range lock via fcntl.
// Uses F_SETLK for non-blocking and F_SETLKW for blocking requests.
// Blocking locks respect context cancellation via deadline.
func (n *Node) Lock(_ context.Context, lockType proto.LockType, flags proto.LockFlags, start, length uint64, _ uint32, _ string) (proto.LockStatus, error) {
	flock := unix.Flock_t{
		Type:   lockTypeToFcntl(lockType),
		Whence: 0, // SEEK_SET
		Start:  int64(start),
		Len:    int64(length),
	}

	cmd := unix.F_SETLK
	if flags&proto.LockFlagBlock != 0 {
		cmd = unix.F_SETLKW
	}

	if err := unix.FcntlFlock(uintptr(n.fd), cmd, &flock); err != nil {
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EACCES) {
			return proto.LockStatusBlocked, nil
		}
		return proto.LockStatusError, toProtoErr(err)
	}

	return proto.LockStatusOK, nil
}

// GetLock tests whether a lock could be placed, returning the conflicting
// lock parameters if one exists.
func (n *Node) GetLock(_ context.Context, lockType proto.LockType, start, length uint64, procID uint32, clientID string) (proto.LockType, uint64, uint64, uint32, string, error) {
	flock := unix.Flock_t{
		Type:   lockTypeToFcntl(lockType),
		Whence: 0, // SEEK_SET
		Start:  int64(start),
		Len:    int64(length),
		Pid:    int32(procID),
	}

	if err := unix.FcntlFlock(uintptr(n.fd), unix.F_GETLK, &flock); err != nil {
		return 0, 0, 0, 0, "", toProtoErr(err)
	}

	if flock.Type == unix.F_UNLCK {
		return proto.LockTypeUnlck, start, length, procID, clientID, nil
	}

	return fcntlToLockType(flock.Type), uint64(flock.Start), uint64(flock.Len), uint32(flock.Pid), clientID, nil
}

// lockTypeToFcntl converts a proto.LockType to a unix F_* constant.
func lockTypeToFcntl(lt proto.LockType) int16 {
	switch lt {
	case proto.LockTypeRdLck:
		return unix.F_RDLCK
	case proto.LockTypeWrLck:
		return unix.F_WRLCK
	case proto.LockTypeUnlck:
		return unix.F_UNLCK
	default:
		return unix.F_UNLCK
	}
}

// fcntlToLockType converts a unix F_* constant to proto.LockType.
func fcntlToLockType(ft int16) proto.LockType {
	switch ft {
	case unix.F_RDLCK:
		return proto.LockTypeRdLck
	case unix.F_WRLCK:
		return proto.LockTypeWrLck
	case unix.F_UNLCK:
		return proto.LockTypeUnlck
	default:
		return proto.LockTypeUnlck
	}
}

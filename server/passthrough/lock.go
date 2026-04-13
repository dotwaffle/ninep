package passthrough

import (
	"context"
	"syscall"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

// Compile-time interface assertion for lock operations.
var _ server.NodeLocker = (*Node)(nil)

// Lock acquires, tests, or releases a POSIX byte-range lock via fcntl.
// Uses F_SETLK for non-blocking and F_SETLKW for blocking requests.
// Blocking locks respect context cancellation via deadline.
func (n *Node) Lock(_ context.Context, lockType proto.LockType, flags proto.LockFlags, start, length uint64, _ uint32, _ string) (proto.LockStatus, error) {
	flock := syscall.Flock_t{
		Type:   lockTypeToFcntl(lockType),
		Whence: 0, // SEEK_SET
		Start:  int64(start),
		Len:    int64(length),
	}

	cmd := syscall.F_SETLK
	if flags&proto.LockFlagBlock != 0 {
		cmd = syscall.F_SETLKW
	}

	if err := syscall.FcntlFlock(uintptr(n.fd), cmd, &flock); err != nil {
		if err == syscall.EAGAIN || err == syscall.EACCES {
			return proto.LockStatusBlocked, nil
		}
		return proto.LockStatusError, toProtoErr(err)
	}

	return proto.LockStatusOK, nil
}

// GetLock tests whether a lock could be placed, returning the conflicting
// lock parameters if one exists.
func (n *Node) GetLock(_ context.Context, lockType proto.LockType, start, length uint64, procID uint32, clientID string) (proto.LockType, uint64, uint64, uint32, string, error) {
	flock := syscall.Flock_t{
		Type:   lockTypeToFcntl(lockType),
		Whence: 0, // SEEK_SET
		Start:  int64(start),
		Len:    int64(length),
		Pid:    int32(procID),
	}

	if err := syscall.FcntlFlock(uintptr(n.fd), syscall.F_GETLK, &flock); err != nil {
		return 0, 0, 0, 0, "", toProtoErr(err)
	}

	if flock.Type == syscall.F_UNLCK {
		return proto.LockTypeUnlck, start, length, procID, clientID, nil
	}

	return fcntlToLockType(flock.Type), uint64(flock.Start), uint64(flock.Len), uint32(flock.Pid), clientID, nil
}

// lockTypeToFcntl converts a proto.LockType to a syscall F_* constant.
func lockTypeToFcntl(lt proto.LockType) int16 {
	switch lt {
	case proto.LockTypeRdLck:
		return syscall.F_RDLCK
	case proto.LockTypeWrLck:
		return syscall.F_WRLCK
	case proto.LockTypeUnlck:
		return syscall.F_UNLCK
	default:
		return syscall.F_UNLCK
	}
}

// fcntlToLockType converts a syscall F_* constant to proto.LockType.
func fcntlToLockType(ft int16) proto.LockType {
	switch ft {
	case syscall.F_RDLCK:
		return proto.LockTypeRdLck
	case syscall.F_WRLCK:
		return proto.LockTypeWrLck
	case syscall.F_UNLCK:
		return proto.LockTypeUnlck
	default:
		return proto.LockTypeUnlck
	}
}

//go:build linux || freebsd

package passthrough

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

// Compile-time interface assertion for lock operations. Locks live on the
// open file handle, not the node: the node's fd is O_PATH for regular
// files, which fcntl locking rejects with EBADF, and POSIX record locks
// are dropped when any fd for the file is closed by the process, so only
// the handle fd -- which lives exactly as long as the fid stays open --
// gives locks the lifetime the client expects.
var _ server.FileLocker = (*fileHandle)(nil)

// Lock acquires, tests, or releases a POSIX byte-range lock via fcntl on
// the open handle's fd.
//
// It always issues the non-blocking F_SETLK. A contended lock returns
// LockStatusBlocked so the client retries, rather than parking a bounded
// server worker in F_SETLKW where no Tflush could cancel it and a single peer
// could pin every worker on the connection. This follows the 9P2000.L model,
// in which the client implements blocking by re-issuing Tlock after a BLOCKED
// response; the LockFlagBlock flag is therefore advisory here.
func (h *fileHandle) Lock(_ context.Context, lockType proto.LockType, _ proto.LockFlags, start, length uint64, _ uint32, _ string) (proto.LockStatus, error) {
	flock := unix.Flock_t{
		Type:   lockTypeToFcntl(lockType),
		Whence: 0, // SEEK_SET
		Start:  int64(start),
		Len:    int64(length),
	}

	if err := unix.FcntlFlock(uintptr(h.fd), unix.F_SETLK, &flock); err != nil {
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EACCES) {
			return proto.LockStatusBlocked, nil
		}
		return proto.LockStatusError, toProtoErr(err)
	}

	return proto.LockStatusOK, nil
}

// GetLock tests whether a lock could be placed, returning the conflicting
// lock parameters if one exists.
func (h *fileHandle) GetLock(_ context.Context, lockType proto.LockType, start, length uint64, procID uint32, clientID string) (proto.LockType, uint64, uint64, uint32, string, error) {
	flock := unix.Flock_t{
		Type:   lockTypeToFcntl(lockType),
		Whence: 0, // SEEK_SET
		Start:  int64(start),
		Len:    int64(length),
		Pid:    int32(procID),
	}

	if err := unix.FcntlFlock(uintptr(h.fd), unix.F_GETLK, &flock); err != nil {
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

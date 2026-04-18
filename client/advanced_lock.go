package client

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/dotwaffle/ninep/proto"
)

// LockType is a thin public enum over [proto.LockType] so callers of the
// client package don't need to import proto/ for the common lock-type
// constants. Per-value equivalence: LockRead == LockType(LockTypeRdLck),
// LockWrite == LockType(LockTypeWrLck), LockUnlock == LockType(LockTypeUnlck).
type LockType proto.LockType

// Lock type constants. These are wire-compatible with proto.LockType;
// the conversion to the proto type is a single uint8 widening.
const (
	// LockRead requests a shared (read) lock. Multiple holders of a
	// read lock over overlapping regions may coexist; a write lock
	// blocks until all outstanding read locks release.
	LockRead LockType = LockType(proto.LockTypeRdLck)

	// LockWrite requests an exclusive (write) lock. At most one holder
	// per overlapping region, and the lock blocks against any concurrent
	// read or write lock.
	LockWrite LockType = LockType(proto.LockTypeWrLck)

	// LockUnlock releases a previously-held lock over a region. Typically
	// issued via [File.Unlock] rather than [File.Lock].
	LockUnlock LockType = LockType(proto.LockTypeUnlck)
)

// defaultLockBackoff is the exponential poll cadence for File.Lock per
// D-09. Chosen to cover uncontended acquisition in the first pair of
// polls (10+20ms) while capping the worst-case wake latency at 500ms.
// After reaching the cap the cadence stays at 500ms indefinitely; the
// caller's ctx controls the upper bound on total wait time.
//
// Tests override via [WithLockPollSchedule].
var defaultLockBackoff = []time.Duration{
	10 * time.Millisecond,
	20 * time.Millisecond,
	40 * time.Millisecond,
	80 * time.Millisecond,
	160 * time.Millisecond,
	320 * time.Millisecond,
	500 * time.Millisecond,
}

// backoffFor returns the sleep duration for iteration i from schedule.
// After len(schedule)-1, returns the last entry (cap). Panics on an
// empty schedule — callers must guarantee len(schedule) >= 1.
func backoffFor(schedule []time.Duration, i int) time.Duration {
	if i >= len(schedule) {
		return schedule[len(schedule)-1]
	}
	return schedule[i]
}

// lockSchedule returns c.lockPollSchedule when configured via
// [WithLockPollSchedule], otherwise [defaultLockBackoff]. The returned
// slice is read-only for the Conn's lifetime.
func (c *Conn) lockSchedule() []time.Duration {
	if len(c.lockPollSchedule) > 0 {
		return c.lockPollSchedule
	}
	return defaultLockBackoff
}

// Lock describes a conflicting POSIX byte-range lock holder as returned
// by [File.GetLock]. A nil *Lock return from GetLock means the queried
// region is free (no conflict); a non-nil *Lock describes the holder
// whose lock conflicts with the proposed request.
type Lock struct {
	// Type is the conflicting holder's lock type (LockRead or LockWrite;
	// LockUnlock is never surfaced — it is the server's "no conflict"
	// signal translated into a nil return).
	Type LockType

	// Start is the byte offset of the conflicting region.
	Start uint64

	// Length is the byte length of the conflicting region; 0 means
	// "to end of file" per POSIX semantics.
	Length uint64

	// ProcID is the holder's process identifier, as registered when the
	// lock was acquired.
	ProcID uint32

	// ClientID is the holder's client identifier. Empty when the holder
	// did not supply one.
	ClientID string
}

// Lock acquires a POSIX-style advisory lock on this File's fid over the
// region [0, end-of-file). Blocks until the lock is acquired, ctx is
// cancelled, or the server returns an error.
//
// The wire-level Tlock is poll-based: the server returns SUCCESS / BLOCKED
// / ERROR / GRACE per call. On BLOCKED or GRACE, Lock sleeps per the
// [WithLockPollSchedule] backoff curve (default 10/20/40/80/160/320/500ms
// cap) and re-issues Tlock until one of the terminal statuses fires.
//
// On ctx cancellation, Lock unconditionally emits a Tlock(LockUnlock)
// cleanup using a fresh background context (with a short deadline) to
// release any lock the server may have granted between our Tlock send and
// ctx firing — Pitfall 6 in 21-RESEARCH.md. This belt-and-braces cleanup
// keeps server state aligned with client state even without Phase 22's
// ctx-driven Tflush wiring.
//
// The fid MUST be opened (via [Conn.OpenFile] or [Conn.Create]); calling
// Lock on a walked-but-unopened fid returns a *Error with the server's
// errno (typically EBADF — server bridge enforces fidOpened state).
//
// Concurrency constraint: Lock and [File.Close] are NOT safe to call
// concurrently on the same *File. Close deliberately bypasses f.mu to
// keep the shutdown path wait-free (Phase 20 D-12), which means a Close
// racing against a blocked Lock (parked in the backoff select) can
// release f.fid to the allocator before unlockCleanup fires. The
// allocator may then re-issue that numeric fid to an unrelated *File,
// and the cleanup Tlock(UNLCK) will land on the wrong fid — at best
// EBADF, at worst releasing a lock held by another caller. The same
// race exists on the in-flight-Tlock ctx-cancel branch. Callers who
// need to cancel a blocked Lock MUST cancel ctx and wait for Lock to
// return before invoking Close. If the caller's lifecycle cannot
// guarantee this, serialise Lock/Close externally with a handle-level
// mutex.
//
// Requires 9P2000.L; returns a wrapped [ErrNotSupported] on a .u Conn.
func (f *File) Lock(ctx context.Context, lt LockType) error {
	if err := f.conn.requireDialect(protocolL, "Lock"); err != nil {
		return err
	}
	schedule := f.conn.lockSchedule()
	r := f.conn.Raw()
	procID := uint32(os.Getpid())
	for i := 0; ; i++ {
		status, err := r.Tlock(ctx, f.fid, proto.LockType(lt), proto.LockFlagBlock,
			0, 0, procID, "")
		if err != nil {
			// Ctx cancel observed during transmission: emit the
			// belt-and-braces UNLCK (Pitfall 6) and surface ctx.Err().
			if ctx.Err() != nil {
				f.unlockCleanup(procID)
				return ctx.Err()
			}
			return err
		}
		switch status {
		case proto.LockStatusOK:
			return nil
		case proto.LockStatusBlocked, proto.LockStatusGrace:
			t := time.NewTimer(backoffFor(schedule, i))
			select {
			case <-t.C:
				// continue the loop; next iteration polls again.
			case <-ctx.Done():
				t.Stop()
				f.unlockCleanup(procID)
				return ctx.Err()
			}
		case proto.LockStatusError:
			// Effectively unreachable against ninep's server (errno
			// comes through Rlerror via toError instead), but handle
			// defensively for interop with peers that set Status=Error.
			return &Error{Errno: proto.EACCES}
		default:
			return fmt.Errorf("client: unknown Rlock.Status %d", status)
		}
	}
}

// unlockCleanup runs a Tlock(UNLCK) on a fresh background context with
// the drain deadline. Used exclusively by Lock's ctx-cancel paths.
// Swallows errors — the caller's ctx.Err() is the meaningful signal.
func (f *File) unlockCleanup(procID uint32) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupDeadline)
	defer cancel()
	_, _ = f.conn.Raw().Tlock(cleanupCtx, f.fid, proto.LockTypeUnlck,
		0, 0, 0, procID, "")
}

// Unlock releases any lock held on this File's fid via Tlock(LockUnlock).
// Idempotent — the server accepts UNLCK on a region with no outstanding
// lock and returns OK.
//
// Requires 9P2000.L; returns a wrapped [ErrNotSupported] on a .u Conn.
func (f *File) Unlock(ctx context.Context) error {
	if err := f.conn.requireDialect(protocolL, "Unlock"); err != nil {
		return err
	}
	_, err := f.conn.Raw().Tlock(ctx, f.fid, proto.LockTypeUnlck,
		0, 0, 0, uint32(os.Getpid()), "")
	return err
}

// TryLock attempts to acquire a lock non-blocking. Returns (true, nil) if
// acquired, (false, nil) if the region is already held by a conflicting
// lock (server returned BLOCKED or GRACE), or (false, err) on protocol
// error.
//
// Issues a single Tlock WITHOUT [proto.LockFlagBlock] — TryLock never
// retries. Callers wanting bounded-retry semantics should compose Lock
// with a ctx deadline instead.
//
// Requires 9P2000.L; returns (false, wrapped [ErrNotSupported]) on a
// .u Conn.
func (f *File) TryLock(ctx context.Context, lt LockType) (bool, error) {
	if err := f.conn.requireDialect(protocolL, "TryLock"); err != nil {
		return false, err
	}
	status, err := f.conn.Raw().Tlock(ctx, f.fid, proto.LockType(lt),
		0, 0, 0, uint32(os.Getpid()), "")
	if err != nil {
		return false, err
	}
	switch status {
	case proto.LockStatusOK:
		return true, nil
	case proto.LockStatusBlocked, proto.LockStatusGrace:
		return false, nil
	case proto.LockStatusError:
		return false, &Error{Errno: proto.EACCES}
	default:
		return false, fmt.Errorf("client: unknown Rlock.Status %d", status)
	}
}

// GetLock queries the server for any lock currently held over the region
// that conflicts with a lock of type lt. Returns a non-nil *Lock
// describing the holder if a conflict exists, (nil, nil) if the region
// is free, or (nil, err) on protocol error.
//
// The server signals "no conflict" by returning LockTypeUnlck in the
// Rgetlock reply; GetLock translates that into a nil pointer so callers
// can branch on `got == nil` without inspecting a sentinel field.
//
// Requires 9P2000.L; returns (nil, wrapped [ErrNotSupported]) on a
// .u Conn.
func (f *File) GetLock(ctx context.Context, lt LockType) (*Lock, error) {
	if err := f.conn.requireDialect(protocolL, "GetLock"); err != nil {
		return nil, err
	}
	rr, err := f.conn.Raw().Tgetlock(ctx, f.fid, proto.LockType(lt),
		0, 0, uint32(os.Getpid()), "")
	if err != nil {
		return nil, err
	}
	if rr.LockType == proto.LockTypeUnlck {
		return nil, nil // region is free
	}
	return &Lock{
		Type:     LockType(rr.LockType),
		Start:    rr.Start,
		Length:   rr.Length,
		ProcID:   rr.ProcID,
		ClientID: rr.ClientID,
	}, nil
}

// -- test hooks -----------------------------------------------------

// DefaultLockBackoff returns the default exponential backoff schedule
// used by [File.Lock]. Exposed as a test-only hook via an exported
// symbol so black-box tests can assert the curve without reaching into
// package internals.
//
// The returned slice is a defensive copy; mutations by the caller do
// not affect the Conn-wide default.
func DefaultLockBackoff() []time.Duration {
	out := make([]time.Duration, len(defaultLockBackoff))
	copy(out, defaultLockBackoff)
	return out
}

// BackoffFor returns the sleep duration for iteration i from schedule.
// Exposed as a test hook to validate cap behavior at i >= len(schedule).
// Production callers should never need this.
func BackoffFor(schedule []time.Duration, i int) time.Duration {
	return backoffFor(schedule, i)
}

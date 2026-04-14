package fstest

import (
	"testing"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
	"github.com/dotwaffle/ninep/server"
)

// LockExpectedTree documents the tree shape a root must provide for
// CheckLock to pass. The root must contain a file "lockfile" that
// implements NodeOpener AND NodeLocker. lockfile must start with no
// active locks; Tgetlock on an un-held file must return LockTypeUnlck.
var LockExpectedTree = map[string]string{
	"lockfile": "implements NodeOpener + NodeLocker; starts un-held",
}

// LockCases is the exported slice of single-connection lock test cases.
//
// Multi-connection contention is NOT covered here: CheckLock verifies
// protocol plumbing between a single client and the NodeLocker
// implementation. For multi-client contention, use newConnPair-based
// tests directly (see server/lock_test.go).
//
// Callers MUST NOT mutate LockCases -- iterate to filter cases.
var LockCases = []TestCase{
	{Name: "lock/acquire-write", Run: testLockAcquireWrite},
	{Name: "lock/release", Run: testLockRelease},
	{Name: "lock/getlock-no-conflict", Run: testLockGetlockNoConflict},
}

// CheckLock runs every LockCases entry against a fresh root obtained
// from newRoot. The root must conform to LockExpectedTree.
//
// CheckLock is opt-in: callers whose filesystem does not support locks
// should NOT call this function. Check() and CheckFactory() do not run
// lock cases -- filesystems without lock support are unaffected.
//
// Scope: single-connection happy path only. Multi-connection contention
// is intentionally excluded because not every filesystem implementation
// supports multi-connection lock semantics; such tests belong in
// implementation-specific test files using newConnPair directly.
func CheckLock(t *testing.T, newRoot func(t *testing.T) server.Node) {
	t.Helper()
	for _, tc := range LockCases {
		t.Run(tc.Name, func(t *testing.T) {
			root := newRoot(t)
			tc.Run(t, root)
		})
	}
}

// testLockAcquireWrite: walk to lockfile, open, Tlock(WrLck) -> OK.
func testLockAcquireWrite(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	msg := walk(t, tc, 2, 0, 1, "lockfile")
	expectRwalk(t, msg)

	msg = lopen(t, tc, 3, 1, 0)
	if _, ok := msg.(*p9l.Rlopen); !ok {
		t.Fatalf("expected Rlopen, got %T: %+v", msg, msg)
	}

	msg = tlock(t, tc, 4, 1, proto.LockTypeWrLck, 0, 0, 100, 1234, "test")
	rl := expectRlock(t, msg)
	if rl.Status != proto.LockStatusOK {
		t.Errorf("lock status = %v, want LockStatusOK", rl.Status)
	}
}

// testLockRelease: acquire write lock, then unlock. Both must return OK.
func testLockRelease(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	msg := walk(t, tc, 2, 0, 1, "lockfile")
	expectRwalk(t, msg)

	if _, ok := lopen(t, tc, 3, 1, 0).(*p9l.Rlopen); !ok {
		t.Fatalf("open failed")
	}

	msg = tlock(t, tc, 4, 1, proto.LockTypeWrLck, 0, 0, 100, 1234, "test")
	if rl := expectRlock(t, msg); rl.Status != proto.LockStatusOK {
		t.Fatalf("initial acquire: status = %v, want OK", rl.Status)
	}

	msg = tlock(t, tc, 5, 1, proto.LockTypeUnlck, 0, 0, 100, 1234, "test")
	if rl := expectRlock(t, msg); rl.Status != proto.LockStatusOK {
		t.Errorf("unlock: status = %v, want OK", rl.Status)
	}
}

// testLockGetlockNoConflict: Tgetlock on an un-held file returns Unlck.
func testLockGetlockNoConflict(t *testing.T, root server.Node) {
	tc := newTestConn(t, root)
	attach(t, tc, 1, 0, "test", "")

	msg := walk(t, tc, 2, 0, 1, "lockfile")
	expectRwalk(t, msg)

	if _, ok := lopen(t, tc, 3, 1, 0).(*p9l.Rlopen); !ok {
		t.Fatalf("open failed")
	}

	msg = tgetlock(t, tc, 4, 1, proto.LockTypeWrLck, 0, 100, 1234, "test")
	rgl := expectRgetlock(t, msg)
	if rgl.LockType != proto.LockTypeUnlck {
		t.Errorf("getlock (no existing lock) type = %v, want LockTypeUnlck", rgl.LockType)
	}
}

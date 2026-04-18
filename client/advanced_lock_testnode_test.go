package client_test

import (
	"context"
	"sync"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

// lockCall captures the arguments observed by a single Lock() invocation
// on testLockerNode. Used by advanced_lock_test.go to assert the Pitfall-6
// belt-and-braces UNLCK fires after a ctx-cancel in File.Lock.
type lockCall struct {
	LockType proto.LockType
	Flags    proto.LockFlags
	Start    uint64
	Length   uint64
	ProcID   uint32
	ClientID string
}

// testLockerNode is a programmable server.Node implementing NodeLocker +
// NodeOpener. Lock() pops statuses from nextStatus in FIFO order; when
// empty, returns LockStatusOK. GetLock() reports the currently-held lock
// state when isHeld is true, LockTypeUnlck otherwise.
//
// Prefix "test" distinguishes from 21-01's minimal rawTestLockerNode.
type testLockerNode struct {
	server.Inode

	mu         sync.Mutex
	calls      []lockCall
	nextStatus []proto.LockStatus
	heldType   proto.LockType
	heldStart  uint64
	heldLength uint64
	heldProcID uint32
	heldClient string
	isHeld     bool
}

// Open satisfies server.NodeOpener. Lock requires a server-side fidOpened
// state (bridge.go handleLock); without Open() the server returns EBADF
// on the Tlock against a walked-but-unopened fid.
func (n *testLockerNode) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return nil, 0, nil
}

// Lock implements server.NodeLocker. Thread-safe: callers from multiple
// test goroutines (1000-iteration leak test) converge here.
func (n *testLockerNode) Lock(_ context.Context, lt proto.LockType, flags proto.LockFlags,
	start, length uint64, procID uint32, clientID string,
) (proto.LockStatus, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, lockCall{
		LockType: lt, Flags: flags,
		Start: start, Length: length,
		ProcID: procID, ClientID: clientID,
	})
	if lt == proto.LockTypeUnlck {
		n.isHeld = false
		return proto.LockStatusOK, nil
	}
	// Non-UNLCK: consume a queued status if one is present.
	if len(n.nextStatus) > 0 {
		s := n.nextStatus[0]
		n.nextStatus = n.nextStatus[1:]
		if s == proto.LockStatusOK {
			n.heldType = lt
			n.heldStart = start
			n.heldLength = length
			n.heldProcID = procID
			n.heldClient = clientID
			n.isHeld = true
		}
		return s, nil
	}
	// Default: grant the lock.
	n.heldType = lt
	n.heldStart = start
	n.heldLength = length
	n.heldProcID = procID
	n.heldClient = clientID
	n.isHeld = true
	return proto.LockStatusOK, nil
}

// GetLock implements server.NodeLocker. Returns the current held state
// verbatim when isHeld, otherwise signals "region free" via LockTypeUnlck.
func (n *testLockerNode) GetLock(_ context.Context, _ proto.LockType,
	start, length uint64, _ uint32, _ string,
) (proto.LockType, uint64, uint64, uint32, string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.isHeld {
		return proto.LockTypeUnlck, start, length, 0, "", nil
	}
	return n.heldType, n.heldStart, n.heldLength, n.heldProcID, n.heldClient, nil
}

// queueStatus appends statuses to the nextStatus queue; subsequent
// non-UNLCK Lock() calls pop and return them in FIFO order.
func (n *testLockerNode) queueStatus(ss ...proto.LockStatus) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.nextStatus = append(n.nextStatus, ss...)
}

// recordedCalls returns a snapshot of all Lock() invocations observed so
// far. The returned slice is independent of the node's internal state.
func (n *testLockerNode) recordedCalls() []lockCall {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]lockCall(nil), n.calls...)
}

// hasUnlock reports whether any Lock() call with LockType=Unlck has been
// recorded. Used by TestClient_Lock_CtxCancel_SendsUnlock for the
// Pitfall-6 assertion without racing against the cleanup goroutine.
func (n *testLockerNode) hasUnlock() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, c := range n.calls {
		if c.LockType == proto.LockTypeUnlck {
			return true
		}
	}
	return false
}

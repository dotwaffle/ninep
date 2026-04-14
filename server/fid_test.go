package server

import (
	"errors"
	"sync"
	"testing"

	"github.com/dotwaffle/ninep/proto"
)

// testNode is a minimal Node implementation for test use.
type testNode struct {
	Inode
}

// newTestNode creates a testNode initialized with the given QID.
func newTestNode(qid proto.QID) *testNode {
	n := &testNode{}
	n.Init(qid, n)
	return n
}

func TestFidTable_AddAndGet(t *testing.T) {
	t.Parallel()

	ft := newFidTable()
	node := newTestNode(proto.QID{Type: proto.QTFILE, Version: 1, Path: 100})
	fs := &fidState{node: node, state: fidAllocated}

	if err := ft.add(1, fs, 0); err != nil {
		t.Fatalf("add fid 1: %v", err)
	}

	got := ft.get(1)
	if got == nil {
		t.Fatal("get fid 1: got nil, want fidState")
	}
	if got.node != node {
		t.Errorf("get fid 1: node = %v, want %v", got.node, node)
	}
	if got.state != fidAllocated {
		t.Errorf("get fid 1: state = %v, want fidAllocated", got.state)
	}
}

func TestFidTable_AddDuplicate(t *testing.T) {
	t.Parallel()

	ft := newFidTable()
	node := newTestNode(proto.QID{Path: 1})
	fs := &fidState{node: node, state: fidAllocated}

	if err := ft.add(1, fs, 0); err != nil {
		t.Fatalf("first add: %v", err)
	}

	err := ft.add(1, fs, 0)
	if err == nil {
		t.Fatal("second add: got nil error, want ErrFidInUse")
	}
	if !isErrFidInUse(err) {
		t.Errorf("second add: got %v, want ErrFidInUse", err)
	}
}

func TestFidTable_GetNonexistent(t *testing.T) {
	t.Parallel()

	ft := newFidTable()
	got := ft.get(42)
	if got != nil {
		t.Errorf("get nonexistent fid: got %v, want nil", got)
	}
}

func TestFidTable_Clunk(t *testing.T) {
	t.Parallel()

	ft := newFidTable()
	node := newTestNode(proto.QID{Path: 2})
	fs := &fidState{node: node, state: fidAllocated}

	if err := ft.add(1, fs, 0); err != nil {
		t.Fatalf("add: %v", err)
	}

	got := ft.clunk(1)
	if got == nil {
		t.Fatal("clunk fid 1: got nil, want fidState")
	}
	if got.node != node {
		t.Error("clunk fid 1: wrong node returned")
	}

	// Subsequent get should return nil.
	if ft.get(1) != nil {
		t.Error("get after clunk: got non-nil, want nil")
	}
}

func TestFidTable_ClunkNonexistent(t *testing.T) {
	t.Parallel()

	ft := newFidTable()
	got := ft.clunk(99)
	if got != nil {
		t.Errorf("clunk nonexistent fid: got %v, want nil", got)
	}
}

func TestFidTable_ClunkAll(t *testing.T) {
	t.Parallel()

	ft := newFidTable()
	for i := range 5 {
		node := newTestNode(proto.QID{Path: uint64(i)})
		if err := ft.add(proto.Fid(i), &fidState{node: node, state: fidAllocated}, 0); err != nil {
			t.Fatalf("add fid %d: %v", i, err)
		}
	}

	all := ft.clunkAll()
	if len(all) != 5 {
		t.Errorf("clunkAll: got %d states, want 5", len(all))
	}

	// Table should be empty now.
	if ft.len() != 0 {
		t.Errorf("len after clunkAll: got %d, want 0", ft.len())
	}
}

func TestFidTable_Update(t *testing.T) {
	t.Parallel()

	ft := newFidTable()
	node1 := newTestNode(proto.QID{Path: 10})
	node2 := newTestNode(proto.QID{Path: 20})

	if err := ft.add(1, &fidState{node: node1, state: fidAllocated}, 0); err != nil {
		t.Fatalf("add: %v", err)
	}

	if !ft.update(1, node2) {
		t.Fatal("update fid 1: got false, want true")
	}

	got := ft.get(1)
	if got == nil {
		t.Fatal("get after update: got nil")
	}
	if got.node != node2 {
		t.Errorf("get after update: node = %v, want %v", got.node, node2)
	}
}

func TestFidTable_UpdateNonexistent(t *testing.T) {
	t.Parallel()

	ft := newFidTable()
	node := newTestNode(proto.QID{Path: 1})
	if ft.update(42, node) {
		t.Error("update nonexistent fid: got true, want false")
	}
}

func TestFidTable_MarkOpened(t *testing.T) {
	t.Parallel()

	ft := newFidTable()
	node := newTestNode(proto.QID{Path: 1})
	if err := ft.add(1, &fidState{node: node, state: fidAllocated}, 0); err != nil {
		t.Fatalf("add: %v", err)
	}

	if !ft.markOpened(1) {
		t.Fatal("markOpened: got false, want true")
	}

	got := ft.get(1)
	if got.state != fidOpened {
		t.Errorf("state after markOpened: got %v, want fidOpened", got.state)
	}

	// Second markOpened should fail (already opened).
	if ft.markOpened(1) {
		t.Error("second markOpened: got true, want false (already opened)")
	}
}

func TestFidTable_MarkOpenedNonexistent(t *testing.T) {
	t.Parallel()

	ft := newFidTable()
	if ft.markOpened(42) {
		t.Error("markOpened nonexistent fid: got true, want false")
	}
}

func TestFidTable_Len(t *testing.T) {
	t.Parallel()

	ft := newFidTable()
	if ft.len() != 0 {
		t.Errorf("initial len: got %d, want 0", ft.len())
	}

	for i := range 3 {
		node := newTestNode(proto.QID{Path: uint64(i)})
		if err := ft.add(proto.Fid(i), &fidState{node: node, state: fidAllocated}, 0); err != nil {
			t.Fatalf("add fid %d: %v", i, err)
		}
	}

	if ft.len() != 3 {
		t.Errorf("len after 3 adds: got %d, want 3", ft.len())
	}

	ft.clunk(1)
	if ft.len() != 2 {
		t.Errorf("len after clunk: got %d, want 2", ft.len())
	}
}

func TestFidTable_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	ft := newFidTable()
	const goroutines = 100

	var wg sync.WaitGroup
	wg.Add(goroutines * 3) // add, get, clunk goroutines

	// Concurrent adds.
	for i := range goroutines {
		go func(id int) {
			defer wg.Done()
			node := newTestNode(proto.QID{Path: uint64(id)})
			_ = ft.add(proto.Fid(id), &fidState{node: node, state: fidAllocated}, 0)
		}(i)
	}

	// Concurrent gets.
	for i := range goroutines {
		go func(id int) {
			defer wg.Done()
			_ = ft.get(proto.Fid(id))
		}(i)
	}

	// Concurrent clunks.
	for i := range goroutines {
		go func(id int) {
			defer wg.Done()
			_ = ft.clunk(proto.Fid(id))
		}(i)
	}

	wg.Wait()

	// Table should be consistent -- len must be non-negative.
	if ft.len() < 0 {
		t.Errorf("len after concurrent access: got negative value %d", ft.len())
	}
}

// isErrFidInUse checks if err wraps ErrFidInUse using errors.Is (GO-ERR-2).
func isErrFidInUse(err error) bool {
	return errors.Is(err, ErrFidInUse)
}

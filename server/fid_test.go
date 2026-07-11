package server

import (
	"context"
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

	prev, ok := ft.update(1, node2, "/n2")
	if !ok {
		t.Fatal("update fid 1: got ok=false, want true")
	}
	if prev != node1 {
		t.Errorf("update displaced %v, want %v", prev, node1)
	}

	got := ft.get(1)
	if got == nil {
		t.Fatal("get after update: got nil")
	}
	if got.node != node2 {
		t.Errorf("get after update: node = %v, want %v", got.node, node2)
	}
}

// TestFidState_ConcurrentUpdateAndRead exercises the exact race an
// in-place Twalk (Fid==NewFid) can produce against any handler reading the
// same fid's node concurrently: fidTable.update must take fs.mu when
// writing, and readers must go through fidState.currentNode, or -race
// flags a torn read/write on the node interface value.
func TestFidState_ConcurrentUpdateAndRead(t *testing.T) {
	t.Parallel()

	ft := newFidTable()
	node1 := newTestNode(proto.QID{Path: 1})
	node2 := newTestNode(proto.QID{Path: 2})
	if err := ft.add(1, &fidState{node: node1, state: fidAllocated}, 0); err != nil {
		t.Fatalf("add: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// Simulates a second in-place Twalk repeatedly rewriting the node.
	go func() {
		defer wg.Done()
		for range 1000 {
			ft.update(1, node2, "/n2")
			ft.update(1, node1, "/n1")
		}
	}()

	// Simulates a concurrent handler reading the node through the locked
	// accessor, as bridge.go handlers do.
	go func() {
		defer wg.Done()
		for range 1000 {
			fs := ft.get(1)
			if fs == nil {
				t.Error("get fid 1: got nil")
				return
			}
			_ = fs.currentNode()
		}
	}()

	wg.Wait()
}

// releaseRecorder is a FileHandle implementing FileReleaser that records
// when Release runs, for asserting release ordering against beginIO/endIO.
type releaseRecorder struct {
	released chan struct{}
}

func (r *releaseRecorder) Release(context.Context) error {
	close(r.released)
	return nil
}

// TestFidState_ClunkDefersReleaseUntilIODrains pins the fix for the fd-reuse
// hazard where Tclunk racing a pipelined Tread on the same fid could call
// FileReleaser.Release while the read was still in flight: beginIO/endIO
// must make handleClunk defer the release until every in-flight I/O call
// registered against the fid has returned.
func TestFidState_ClunkDefersReleaseUntilIODrains(t *testing.T) {
	t.Parallel()

	handle := &releaseRecorder{released: make(chan struct{})}
	fs := &fidState{node: newTestNode(proto.QID{Path: 1}), state: fidOpened, handle: handle}

	if !fs.beginIO() {
		t.Fatal("beginIO: got false, want true (fid not yet closing)")
	}

	// Simulate handleClunk: mark closing, and release only if no I/O call
	// is in flight. One call (started above) is in flight, so the release
	// must not happen here.
	fs.mu.Lock()
	fs.closing = true
	deferRelease := fs.ioRefs > 0
	fs.mu.Unlock()
	if !deferRelease {
		t.Fatal("handleClunk: expected release to be deferred while beginIO call is in flight")
	}

	select {
	case <-handle.released:
		t.Fatal("Release called while an I/O call registered via beginIO was still in flight")
	default:
	}

	// A second beginIO call after closing must be rejected: the caller must
	// not touch handle/node once handleClunk has run.
	if fs.beginIO() {
		t.Fatal("beginIO after closing: got true, want false")
	}

	// The in-flight call finishes and calls endIO: since it was the last
	// one and the fid is closing, this must perform the deferred release.
	fs.endIO(context.Background(), nil)

	select {
	case <-handle.released:
	default:
		t.Fatal("endIO did not release the handle after the last in-flight I/O call drained")
	}
}

// closeRecorderNode is a Node that records whether Close was called, for
// asserting the closeNode gate in fidState.releaseNow/finishClunk.
type closeRecorderNode struct {
	Inode
	closed chan struct{}
}

func newCloseRecorderNode(qid proto.QID) *closeRecorderNode {
	n := &closeRecorderNode{closed: make(chan struct{})}
	n.Init(qid, n)
	return n
}

func (n *closeRecorderNode) Close(context.Context) error {
	close(n.closed)
	return nil
}

// TestFidState_FinishClunk_CloseNodeGate pins the fix for the double-close
// hazard where two fids alias the identical Node value (walk-clone via
// Twalk nwname=0, or an xattrwalk fid): clunking one must not close the
// shared underlying resource while the other is still live. finishClunk's
// closeNode parameter (derived from decRefNode's "was this the last
// reference" result) gates NodeCloser.Close for exactly this reason.
func TestFidState_FinishClunk_CloseNodeGate(t *testing.T) {
	t.Parallel()

	node := newCloseRecorderNode(proto.QID{Path: 1})

	// First aliasing fid clunks with closeNode=false: another fid (fs2,
	// constructed below) still references the same node.
	fs1 := &fidState{node: node, state: fidOpened}
	fs1.finishClunk(context.Background(), nil, false)
	select {
	case <-node.closed:
		t.Fatal("Close called with closeNode=false; want deferred to the last alias's clunk")
	default:
	}

	// Second (and last) aliasing fid clunks with closeNode=true.
	fs2 := &fidState{node: node, state: fidOpened}
	fs2.finishClunk(context.Background(), nil, true)
	select {
	case <-node.closed:
	default:
		t.Fatal("Close not called with closeNode=true (last reference)")
	}
}

// TestFidState_ClunkDefersCloseNodeGate covers the same closeNode gate on
// the deferred-release path: finishClunk records closeNode on fs while an
// I/O call is still in flight, and endIO must honor that recorded value
// (not unconditionally close) once it performs the deferred release.
func TestFidState_ClunkDefersCloseNodeGate(t *testing.T) {
	t.Parallel()

	node := newCloseRecorderNode(proto.QID{Path: 2})
	fs := &fidState{node: node, state: fidOpened}

	if !fs.beginIO() {
		t.Fatal("beginIO: got false, want true (fid not yet closing)")
	}

	// closeNode=false: an aliasing fid is still live, so even once the
	// in-flight I/O call drains, the deferred release must not close node.
	fs.finishClunk(context.Background(), nil, false)

	fs.endIO(context.Background(), nil)
	select {
	case <-node.closed:
		t.Fatal("endIO closed node despite closeNode=false recorded by finishClunk")
	default:
	}
}

// TestFidTable_UpdateRefTransferBalance drives two goroutines through the
// in-place-walk refcount transfer protocol (inc the new node, dec the node
// update displaced) and asserts the total live count stays balanced. The
// pre-fix protocol read the old node before the swap, so two concurrent
// transfers could dec the same displaced node twice and leak a count on
// the other, which this test makes probable enough to catch.
func TestFidTable_UpdateRefTransferBalance(t *testing.T) {
	t.Parallel()

	ft := newFidTable()
	node1 := newTestNode(proto.QID{Path: 1})
	node2 := newTestNode(proto.QID{Path: 2})
	if err := ft.add(1, &fidState{node: node1, state: fidAllocated}, 0); err != nil {
		t.Fatalf("add: %v", err)
	}
	incRefNode(node1) // The fid's own reference.

	var wg sync.WaitGroup
	for _, target := range []Node{node1, node2} {
		wg.Go(func() {
			for range 1000 {
				if prev, ok := ft.update(1, target, "/t"); ok {
					incRefNode(target)
					decRefNode(prev)
				}
			}
		})
	}
	wg.Wait()

	bound := ft.get(1).currentNode()
	var other Node = node1
	if bound == other {
		other = node2
	}
	if got := bound.(InodeEmbedder).EmbeddedInode().refs.Load(); got != 1 {
		t.Errorf("bound node refs = %d, want 1", got)
	}
	if got := other.(InodeEmbedder).EmbeddedInode().refs.Load(); got != 0 {
		t.Errorf("displaced node refs = %d, want 0", got)
	}
}

func TestFidTable_UpdateNonexistent(t *testing.T) {
	t.Parallel()

	ft := newFidTable()
	node := newTestNode(proto.QID{Path: 1})
	if _, ok := ft.update(42, node, "/x"); ok {
		t.Error("update nonexistent fid: got ok=true, want false")
	}
}

func TestFidTable_MarkOpened(t *testing.T) {
	t.Parallel()

	ft := newFidTable()
	node := newTestNode(proto.QID{Path: 1})
	if err := ft.add(1, &fidState{node: node, state: fidAllocated}, 0); err != nil {
		t.Fatalf("add: %v", err)
	}

	if !ft.markOpenedWithHandle(1, nil) {
		t.Fatal("markOpenedWithHandle: got false, want true")
	}

	got := ft.get(1)
	if got.state != fidOpened {
		t.Errorf("state after markOpenedWithHandle: got %v, want fidOpened", got.state)
	}

	// Second markOpened should fail (already opened).
	if ft.markOpenedWithHandle(1, nil) {
		t.Error("second markOpenedWithHandle: got true, want false (already opened)")
	}
}

func TestFidTable_MarkOpenedNonexistent(t *testing.T) {
	t.Parallel()

	ft := newFidTable()
	if ft.markOpenedWithHandle(42, nil) {
		t.Error("markOpenedWithHandle nonexistent fid: got true, want false")
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

// isErrFidInUse reports whether err wraps ErrFidInUse.
func isErrFidInUse(err error) bool {
	return errors.Is(err, ErrFidInUse)
}

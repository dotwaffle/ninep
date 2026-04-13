package server

import (
	"fmt"
	"sync"

	"github.com/dotwaffle/ninep/proto"
)

// fidStatus tracks the lifecycle state of a fid.
type fidStatus uint8

const (
	fidAllocated  fidStatus = iota // Assigned by attach or walk, not yet opened.
	fidOpened                      // Opened via Tlopen/Topen.
	fidXattrRead                   // After xattrwalk: fid holds cached xattr data for reading.
	fidXattrWrite                  // After xattrcreate: fid accumulates writes, commits on clunk.
)

// fidState holds the server-side state for a single fid.
type fidState struct {
	node      Node
	state     fidStatus
	handle    FileHandle     // Non-nil after Open returns a handle (per API-04).
	dirCache  []proto.Dirent // Cached dirents for simple Readdirer (offset tracking).
	dirCached bool           // True after first readdir populates cache.

	// Xattr fields (used when state is fidXattrRead or fidXattrWrite).
	xattrNode   Node        // Original node the xattr belongs to.
	xattrName   string      // Attribute name.
	xattrData   []byte      // Buffer: cached value (read) or accumulated writes (write).
	xattrSize   uint64      // Declared size from xattrcreate.
	xattrFlags  uint32      // Flags from xattrcreate (XATTR_CREATE, XATTR_REPLACE).
	xattrWriter XattrWriter // Non-nil when RawXattrer is in use for writes.
}

// fidTable is a concurrent-safe mapping from fid numbers to their state.
// Protected by sync.RWMutex per GO-CC-3.
type fidTable struct {
	mu   sync.RWMutex
	fids map[proto.Fid]*fidState
}

// newFidTable creates an empty fid table.
func newFidTable() *fidTable {
	return &fidTable{fids: make(map[proto.Fid]*fidState)}
}

// get returns the state for fid, or nil if not present. Safe for concurrent use.
func (ft *fidTable) get(fid proto.Fid) *fidState {
	ft.mu.RLock()
	defer ft.mu.RUnlock()
	return ft.fids[fid]
}

// add inserts a new fid into the table. Returns ErrFidInUse (wrapped) if the
// fid is already present. Safe for concurrent use.
func (ft *fidTable) add(fid proto.Fid, fs *fidState) error {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if _, exists := ft.fids[fid]; exists {
		return fmt.Errorf("add fid %d: %w", fid, ErrFidInUse)
	}
	ft.fids[fid] = fs
	return nil
}

// clunk removes a fid from the table and returns its state. Returns nil if the
// fid was not present. Safe for concurrent use.
func (ft *fidTable) clunk(fid proto.Fid) *fidState {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	fs, ok := ft.fids[fid]
	if !ok {
		return nil
	}
	delete(ft.fids, fid)
	return fs
}

// clunkAll removes all fids from the table and returns their states. The map
// is swapped to an empty map under the lock, then the old entries are collected
// outside the lock to avoid blocking concurrent operations during iteration.
// Safe for concurrent use.
func (ft *fidTable) clunkAll() []*fidState {
	ft.mu.Lock()
	old := ft.fids
	ft.fids = make(map[proto.Fid]*fidState)
	ft.mu.Unlock()

	states := make([]*fidState, 0, len(old))
	for _, fs := range old {
		states = append(states, fs)
	}
	return states
}

// update replaces the node on an existing fid. Returns false if the fid is not
// present. Safe for concurrent use.
func (ft *fidTable) update(fid proto.Fid, node Node) bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	fs, ok := ft.fids[fid]
	if !ok {
		return false
	}
	fs.node = node
	return true
}

// markOpened transitions a fid from fidAllocated to fidOpened. Returns false if
// the fid is not present or is already opened. Safe for concurrent use.
func (ft *fidTable) markOpened(fid proto.Fid) bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	fs, ok := ft.fids[fid]
	if !ok || fs.state != fidAllocated {
		return false
	}
	fs.state = fidOpened
	return true
}

// markOpenedWithHandle transitions a fid from fidAllocated to fidOpened and
// stores the FileHandle. Returns false if the fid is not present or is already
// opened. Safe for concurrent use.
func (ft *fidTable) markOpenedWithHandle(fid proto.Fid, h FileHandle) bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	fs, ok := ft.fids[fid]
	if !ok || fs.state != fidAllocated {
		return false
	}
	fs.state = fidOpened
	fs.handle = h
	return true
}

// updateAndOpen atomically replaces the node, transitions the fid to fidOpened,
// and stores the FileHandle. Returns false if the fid is not present or is not
// in fidAllocated state. Safe for concurrent use.
func (ft *fidTable) updateAndOpen(fid proto.Fid, node Node, h FileHandle) bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	fs, ok := ft.fids[fid]
	if !ok || fs.state != fidAllocated {
		return false
	}
	fs.node = node
	fs.state = fidOpened
	fs.handle = h
	return true
}

// len returns the number of fids in the table. Safe for concurrent use.
func (ft *fidTable) len() int {
	ft.mu.RLock()
	defer ft.mu.RUnlock()
	return len(ft.fids)
}

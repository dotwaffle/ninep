package server

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
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
//
// All access to the fidStatus, handle, dirCache, dirCached, and xattr
// fields must happen with mu held. The fidTable lifecycle methods
// (markOpenedWithHandle, updateAndOpen) nest mu inside
// ft.mu when mutating state so that bridge readers using mu see a
// consistent view.
type fidState struct {
	mu        sync.Mutex // Protects state transitions, xattr, and dir fields.
	node      Node
	path      string // Walked filesystem path for observability; protected by mu like node.
	state     fidStatus
	handle    FileHandle     // Non-nil after Open returns a handle.
	dirCache  []proto.Dirent // Cached dirents for simple Readdirer (offset tracking).
	dirCached bool           // True after first readdir populates cache.

	// ioRefs and closing coordinate handleClunk against in-flight I/O
	// handlers (handleRead, handleWrite, handleReaddir, handleFsync) that
	// dereference handle/node without holding mu for the duration of the
	// syscall. Without this, Tclunk racing a pipelined Tread on the same
	// fid could call FileReleaser.Release/NodeCloser.Close while the read
	// is still in progress; if the OS reused the closed fd number for an
	// unrelated file before the read completed, the read would silently
	// return data from the wrong file. beginIO/endIO ensure the release
	// only happens once every in-flight I/O call on this fid has returned.
	ioRefs  int
	closing bool

	// closeNode records, for the deferred releaseNow call endIO makes once
	// ioRefs drains, whether that call should invoke NodeCloser.Close on
	// node. finishClunk sets this from decRefNode's return value: closing
	// is only correct once no other fid (walk-clone or xattrwalk alias)
	// still references the identical Node value.
	closeNode bool

	// Xattr fields (used when state is fidXattrRead or fidXattrWrite).
	xattrNode   Node        // Original node the xattr belongs to.
	xattrName   string      // Attribute name.
	xattrData   []byte      // Buffer: cached value (read) or accumulated writes (write).
	xattrSize   uint64      // Declared size from xattrcreate.
	xattrFlags  uint32      // Flags from xattrcreate (XATTR_CREATE, XATTR_REPLACE).
	xattrWriter XattrWriter // Non-nil when RawXattrer is in use for writes.
}

// currentState returns fs.state with proper locking. Callers that already
// hold fs.mu should read fs.state directly instead.
func (fs *fidState) currentState() fidStatus {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.state
}

// currentNode returns fs.node with proper locking. The read is synchronized
// against fidTable.update, which rewrites fs.node under fs.mu during an
// in-place Twalk (Fid==NewFid) or a create/mkdir-style updateAndOpen.
// Reading fs.node directly after get() has released the table lock is a
// data race on the interface value: conforming clients serialize requests
// per fid so the race is rarely observed, but nothing at the protocol
// level prevents a pipelining client from walking a fid in place while
// another request against the same fid is in flight. Callers that already
// hold fs.mu should read fs.node directly instead.
func (fs *fidState) currentNode() Node {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.node
}

// currentPath returns fs.path with proper locking; see currentNode for why
// the locked read matters against in-place Twalk rewrites.
func (fs *fidState) currentPath() string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.path
}

// nodeAndPath returns fs.node and fs.path from one critical section, so a
// walk-clone cannot capture the node of one in-place rewrite paired with
// the path of another.
func (fs *fidState) nodeAndPath() (Node, string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.node, fs.path
}

// beginIO registers the caller as about to dereference fs.handle or
// fs.node for a blocking I/O call. It reports false if the fid has
// already been clunked, in which case the caller must return EBADF
// without touching handle or node -- either may already be released.
// Every successful beginIO must be matched by exactly one endIO call.
func (fs *fidState) beginIO() bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.closing {
		return false
	}
	fs.ioRefs++
	return true
}

// endIO unregisters a beginIO call. If handleClunk ran while this call was
// in flight, it deferred the handle release and node close to avoid
// closing them out from under the still-running I/O; if this is the last
// outstanding call on the fid, endIO performs that deferred release now.
func (fs *fidState) endIO(ctx context.Context, logger *slog.Logger) {
	fs.mu.Lock()
	fs.ioRefs--
	release := fs.closing && fs.ioRefs == 0
	closeNode := fs.closeNode
	fs.mu.Unlock()
	if release {
		fs.releaseNow(ctx, logger, closeNode)
	}
}

// releaseNow calls FileReleaser.Release for fs, and NodeCloser.Close on
// fs.node when closeNode is true. It must only run once no I/O call
// registered via beginIO is still in flight, either because handleClunk
// found ioRefs already at zero or because endIO is draining the last
// outstanding call.
//
// closeNode must be false whenever another fid still references the
// identical Node value (a walk-clone or xattrwalk alias of fs.node):
// closing unconditionally would tear down the shared underlying resource
// (e.g. a passthrough fd) out from under that other fid. Callers derive it
// from decRefNode's return value.
func (fs *fidState) releaseNow(ctx context.Context, logger *slog.Logger, closeNode bool) {
	releaseFileHandle(ctx, fs.handle, logger)
	if !closeNode {
		return
	}
	if closer, ok := fs.node.(NodeCloser); ok {
		if err := closer.Close(ctx); err != nil {
			if logger != nil {
				logger.Debug("node close error on clunk", slog.Any("error", err))
			}
		}
	}
}

// finishClunk marks fs as closing and releases its handle/node immediately
// unless a beginIO-registered I/O call is still in flight, in which case
// the last matching endIO performs the release once ioRefs drains to zero.
// Used by handleClunk and handleRemove: both fully tear down the fid --
// Tremove clunks its fid whether or not the remove itself succeeds, per
// remove(5).
//
// closeNode is forwarded to releaseNow (see its doc) and must reflect
// whether this fid is the last one referencing fs.node, as reported by
// decRefNode.
func (fs *fidState) finishClunk(ctx context.Context, logger *slog.Logger, closeNode bool) {
	fs.mu.Lock()
	fs.closing = true
	fs.closeNode = closeNode
	release := fs.ioRefs == 0
	fs.mu.Unlock()
	if release {
		fs.releaseNow(ctx, logger, closeNode)
	}
}

// fidTable is a concurrent-safe mapping from fid numbers to their state.
// Protected by sync.RWMutex.
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

// add inserts a new fid into the table. If maxFids > 0 and the current fid
// count is at or above the cap, add returns ErrFidLimitExceeded and does not
// insert. Returns ErrFidInUse (wrapped) if the fid is already present. The
// cap check and dup check both run inside the write lock, making enforcement
// race-free. Safe for concurrent use.
func (ft *fidTable) add(fid proto.Fid, fs *fidState, maxFids int) error {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if maxFids > 0 && len(ft.fids) >= maxFids {
		return ErrFidLimitExceeded
	}
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

	return slices.Collect(maps.Values(old))
}

// setPath updates the walked filesystem path on an existing fid. No-op if
// the fid is not present. Safe for concurrent use; the write goes through
// the per-fid mutex like every other path access.
func (ft *fidTable) setPath(fid proto.Fid, p string) {
	fs := ft.get(fid)
	if fs == nil {
		return
	}
	fs.mu.Lock()
	fs.path = p
	fs.mu.Unlock()
}

// getPath returns the path recorded for fid, or "" if the fid is absent.
// The path lives under the per-fid mutex (like node), so the table lock is
// only needed for the map lookup; lock ordering ft.mu -> fs.mu matches the
// lifecycle methods. This runs per request from the OTel middleware, so it
// deliberately holds the shared table lock for as little as the lookup.
func (ft *fidTable) getPath(fid proto.Fid) string {
	fs := ft.get(fid)
	if fs == nil {
		return ""
	}
	return fs.currentPath()
}

// update replaces the node and path on an existing fid, returning the node
// it displaced. Returns ok=false (and takes no action) if the fid is not
// present. Safe for concurrent use.
//
// fs.mu is nested inside ft.mu, matching markOpened/updateAndOpen, so the
// write is synchronized against fidState.currentNode's locked read.
// Returning the displaced node from inside the critical section is what
// lets the caller transfer node refcounts atomically: a read-then-swap
// against two pipelined in-place walks on the same fid can observe the
// same old node twice and double-decrement it, prematurely closing a node
// another fid still aliases.
func (ft *fidTable) update(fid proto.Fid, node Node, path string) (Node, bool) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	fs, ok := ft.fids[fid]
	if !ok {
		return nil, false
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	prev := fs.node
	fs.node = node
	fs.path = path
	return prev, true
}

// markOpenedWithHandle transitions a fid from fidAllocated to fidOpened and
// stores the FileHandle. Returns false if the fid is not present or is already
// opened. Safe for concurrent use.
//
// fs.mu is nested inside ft.mu so that the state mutation is visible to
// bridge handlers that read fs.state under fs.mu. Lock ordering is
// ft.mu -> fs.mu; callers holding fs.mu must not attempt to acquire ft.mu.
func (ft *fidTable) markOpenedWithHandle(fid proto.Fid, h FileHandle) bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	fs, ok := ft.fids[fid]
	if !ok {
		return false
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.state != fidAllocated {
		return false
	}
	fs.state = fidOpened
	fs.handle = h
	return true
}

// updateAndOpen atomically replaces the node, transitions the fid to fidOpened,
// and stores the FileHandle, returning the node it displaced. Returns ok=false
// (and takes no action) if the fid is not present or is not in fidAllocated
// state. Safe for concurrent use. Lock ordering matches markOpenedWithHandle; the
// displaced node is returned for the same atomic refcount-transfer reason
// as update.
func (ft *fidTable) updateAndOpen(fid proto.Fid, node Node, h FileHandle) (Node, bool) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	fs, ok := ft.fids[fid]
	if !ok {
		return nil, false
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.state != fidAllocated {
		return nil, false
	}
	prev := fs.node
	fs.node = node
	fs.state = fidOpened
	fs.handle = h
	return prev, true
}

// len returns the number of fids in the table. Safe for concurrent use.
func (ft *fidTable) len() int {
	ft.mu.RLock()
	defer ft.mu.RUnlock()
	return len(ft.fids)
}

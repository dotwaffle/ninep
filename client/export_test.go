package client

import (
	"github.com/dotwaffle/ninep/proto"
)

// RegisterStuckCaller is a test-only hook. It bumps callerWG and registers
// a dummy high-numbered tag in inflightMap, simulating a caller goroutine
// parked somewhere unreachable by signalShutdown (e.g. a custom blocking
// operation with no ctx/closeCh select). The returned release function
// must be called before the test ends to unwind callerWG.
//
// Only exposed to the external client_test package via the _test.go
// suffix. Not part of the public API surface.
func RegisterStuckCaller(c *Conn) func() {
	c.callerWG.Add(1)
	// Use a tag far above the allocator's range (NoTag-1) so there's no
	// collision with real ops.
	tag := proto.Tag(0xFFFE)
	_ = c.inflight.register(tag)
	released := false
	return func() {
		if released {
			return
		}
		released = true
		c.inflight.unregister(tag)
		c.callerWG.Done()
	}
}

// InflightLen returns the current inflight map size. Test-only visibility
// hook for stress/leak tests that assert the map drains to zero.
func InflightLen(c *Conn) int {
	return c.inflight.len()
}

// FreeTagCount returns the number of currently available tags in the
// allocator's free-list. Test-only hook for tag-reuse stress tests.
func FreeTagCount(c *Conn) int {
	return len(c.tags.free)
}

// FidReuseLen returns the depth of the Conn's fid-allocator reuse
// cache. Test-only hook for leak assertions (e.g. "did a failed Walk
// release its reserved fid?"). Not part of the public API.
func FidReuseLen(c *Conn) int {
	return c.fids.len()
}

// SetCachedSize is a test-only helper that pokes the cachedSize field
// on a *File. Used by file_seek_test.go to exercise the SeekEnd code
// path before Phase 21's File.Sync ships. Takes f.mu to match the
// locking discipline of the I/O methods that read cachedSize.
func SetCachedSize(f *File, size int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cachedSize = size
}

// MaxChunk returns the effective maxChunk() clamp on *File. Test-only
// hook used to assert the chunked Read/Write/ReadAt/WriteAt paths
// actually loop (len(buf) > maxChunk() precondition).
func MaxChunk(f *File) uint32 {
	return f.maxChunk()
}

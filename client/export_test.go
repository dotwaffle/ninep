package client

import (
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9u"
)

// AttrToFileInfoForTest exposes the attrToFileInfo conversion helper to
// the external client_test package so the conversion tests can exercise
// the .L Attr -> FileInfo mapping directly without a round-trip.
func AttrToFileInfoForTest(a proto.Attr) FileInfo { return attrToFileInfo(a) }

// StatToFileInfoForTest exposes the statToFileInfo conversion helper to
// the external client_test package so the conversion tests can exercise
// the .u Stat -> FileInfo mapping directly without a round-trip.
func StatToFileInfoForTest(st p9u.Stat) FileInfo { return statToFileInfo(st) }

// RegisterStuckCaller is a test-only hook. It bumps callerWG and registers
// a dummy high-numbered tag in inflightMap, simulating a caller goroutine
// parked somewhere unreachable by signalShutdown (e.g. a custom blocking
// operation with no ctx/closeCh select). The returned release function
// must be called before the test ends to unwind callerWG.
//
// Only exposed to the external client_test package via the _test.go
// suffix. Not part of the public API surface.
func RegisterStuckCaller(c *Conn) func() {
	if !c.beginCall() {
		return func() {}
	}
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
		c.endCall()
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
// on a *File. Useful for tests that want to assert Sync's error path
// does NOT overwrite a pre-existing cachedSize. Takes f.mu to match
// the locking discipline of the I/O methods that read cachedSize.
func SetCachedSize(f *File, size int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cachedSize = size
}

// CachedSizeOf exposes f.cachedSize for Sync tests that assert the
// real wire-backed Sync populates cachedSize. Takes f.mu to match the
// locking discipline of SetCachedSize above.
func CachedSizeOf(f *File) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cachedSize
}

// MaxChunk returns the effective maxChunk() clamp on *File. Test-only
// hook used to assert the chunked Read/Write/ReadAt/WriteAt paths
// actually loop (len(buf) > maxChunk() precondition).
func MaxChunk(f *File) uint32 {
	return f.maxChunk()
}

// NewFileForTest constructs a *File wrapping c with a synthetic fid.
// Used by dialect-gate tests that need a *File handle but do not want to
// drive a full Attach -- the requireDialect gate fires at the ops entry
// before any wire op. Not part of the public API surface.
func NewFileForTest(c *Conn) *File {
	return newFile(c, proto.Fid(0), proto.QID{}, 0)
}

// NewFileWrappingFidForTest constructs a *File bound to an explicit,
// caller-supplied live fid (i.e. one already bound server-side via
// Attach/Walk/Lopen). Used by timeout tests to exercise File.Read /
// File.ReadCtx against the flushMockServer harness, which doesn't
// have OpenFile path semantics. Not part of the public API surface.
func NewFileWrappingFidForTest(c *Conn, fid proto.Fid, iounit uint32) *File {
	return newFile(c, fid, proto.QID{}, iounit)
}

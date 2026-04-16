package server

import (
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// msgCacheCap is the per-type cache depth. Matches hugelgupf/p9's
// maxCacheSize. Three is enough to hold the few in-flight messages a
// typical dispatch loop has in the receive→decode→handle pipeline
// without accumulating unused memory.
const msgCacheCap = 3

// Bounded channel caches for hot request message types. Unlike sync.Pool,
// channels have no cross-P balancing — the 15% regression we observed when
// sync.Pool'ing *proto.Tread (see quick task 260416-4t0) came from sync.Pool
// stealing across Ps under the goroutine-per-request model. Bounded channels
// side-step that entirely. This pattern matches hugelgupf/p9's registry:
//
//   Get: non-blocking receive (cached struct if available, else fresh alloc)
//   Put: non-blocking send      (cache if slot free, else drop to GC)
//
// Package-global is safe because the access pattern is Get from conn.readLoop
// (single goroutine) and Put from handleWorkItem (pool of worker goroutines,
// bounded by maxInflight per connection). Channel send/recv is atomic.
//
// Cached types are the hot-path 9P requests:
//   - Tread, Twrite (data I/O — bulk of traffic)
//   - Twalk, Tclunk (every open/close cycle)
//   - Tlopen, Tgetattr (file metadata lifecycle)
//
// Less-common types (Tattach, Tauth, Tflush, etc.) skip the cache to keep
// the type switch short.
var (
	treadCache    = make(chan *proto.Tread, msgCacheCap)
	twriteCache   = make(chan *proto.Twrite, msgCacheCap)
	twalkCache    = make(chan *proto.Twalk, msgCacheCap)
	tclunkCache   = make(chan *proto.Tclunk, msgCacheCap)
	tlopenCache   = make(chan *p9l.Tlopen, msgCacheCap)
	tgetattrCache = make(chan *p9l.Tgetattr, msgCacheCap)
)

// getCachedTread returns a *proto.Tread from the cache (zeroed) or a fresh
// allocation if the cache is empty. Safe for concurrent use; the non-blocking
// receive falls through to the default case if no cached entry is available.
func getCachedTread() *proto.Tread {
	select {
	case m := <-treadCache:
		*m = proto.Tread{}
		return m
	default:
		return &proto.Tread{}
	}
}

func getCachedTwrite() *proto.Twrite {
	select {
	case m := <-twriteCache:
		*m = proto.Twrite{}
		return m
	default:
		return &proto.Twrite{}
	}
}

func getCachedTwalk() *proto.Twalk {
	select {
	case m := <-twalkCache:
		*m = proto.Twalk{}
		return m
	default:
		return &proto.Twalk{}
	}
}

func getCachedTclunk() *proto.Tclunk {
	select {
	case m := <-tclunkCache:
		*m = proto.Tclunk{}
		return m
	default:
		return &proto.Tclunk{}
	}
}

func getCachedTlopen() *p9l.Tlopen {
	select {
	case m := <-tlopenCache:
		*m = p9l.Tlopen{}
		return m
	default:
		return &p9l.Tlopen{}
	}
}

func getCachedTgetattr() *p9l.Tgetattr {
	select {
	case m := <-tgetattrCache:
		*m = p9l.Tgetattr{}
		return m
	default:
		return &p9l.Tgetattr{}
	}
}

// putCachedMsg returns msg to its type-specific cache if one exists, via a
// non-blocking send. No-op for types not in the cache set or when the cache
// is full. Called from handleWorkItem's defer after the handler has finished
// reading the request.
//
// Twrite.Data is explicitly cleared because it aliased a pooled buffer that
// is being returned to bufpool by this same defer; leaving the slice pointing
// at a recycled bucket buffer would let the next cached-Twrite holder see
// stale / corrupted Data on decode failure between nwname and data read.
func putCachedMsg(msg proto.Message) {
	switch m := msg.(type) {
	case *proto.Tread:
		select {
		case treadCache <- m:
		default:
		}
	case *proto.Twrite:
		m.Data = nil
		select {
		case twriteCache <- m:
		default:
		}
	case *proto.Twalk:
		// Names is overwritten (make) in DecodeFrom so no zeroing needed.
		select {
		case twalkCache <- m:
		default:
		}
	case *proto.Tclunk:
		select {
		case tclunkCache <- m:
		default:
		}
	case *p9l.Tlopen:
		select {
		case tlopenCache <- m:
		default:
		}
	case *p9l.Tgetattr:
		select {
		case tgetattrCache <- m:
		default:
		}
	}
}

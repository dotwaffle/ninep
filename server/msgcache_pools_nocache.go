//go:build nocache

// Package server msgcache_pools_nocache.go — no-cache companion for the
// Phase 13 -tags nocache A/B bench axis (PERF-05.1). Under this build tag
// no caches are declared; putCachedMsg is a no-op and newMessage allocates
// fresh structs on every call (see the call sites in conn.go — they still
// reference {t*}Cache.Get(), so this file supplies zero-value pool.Cache[T]
// instances whose nil channel makes Get fall through to new(T) via the
// default channel-recv branch).
//
// The trick: a zero-value pool.Cache[T]{} has ch == nil. A nil channel's
// recv and send both block forever — in a non-blocking
// `select { case <-c.ch: ...; default: ...; }` both paths correctly fall
// through the default arm. This preserves the nocache semantic (no cached
// reuse) without changing the call-site shape in conn.go.
//
// MUST NOT ship in production binaries; default build (no -tags) uses the
// cap-3 channels in msgcache_pools.go instead.

package server

import (
	"github.com/dotwaffle/ninep/internal/pool"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// Zero-value pool.Cache[T] (ch == nil): every Get falls through to new(T);
// every Put drops to GC. Semantically equivalent to the pre-extraction
// nocache fresh-alloc getters, using the same Cache[T] API so conn.go call
// sites compile under both build tags.
//
// We cannot use pool.NewCache (which bakes in Cap=3). We construct the
// zero-value Cache[T] directly; its nil channel forces recv/send to block
// immediately, falling through the default branch on both sides.
var (
	treadCache    = &pool.Cache[proto.Tread]{}
	twriteCache   = &pool.Cache[proto.Twrite]{}
	twalkCache    = &pool.Cache[proto.Twalk]{}
	tclunkCache   = &pool.Cache[proto.Tclunk]{}
	tlopenCache   = &pool.Cache[p9l.Tlopen]{}
	tgetattrCache = &pool.Cache[p9l.Tgetattr]{}
	tlcreateCache = &pool.Cache[p9l.Tlcreate]{}
	tremoveCache  = &pool.Cache[proto.Tremove]{}
	tmkdirCache   = &pool.Cache[p9l.Tmkdir]{}
	tsetattrCache = &pool.Cache[p9l.Tsetattr]{}
	trenameCache  = &pool.Cache[p9l.Trename]{}
	tsymlinkCache = &pool.Cache[p9l.Tsymlink]{}
	tmknodCache   = &pool.Cache[p9l.Tmknod]{}
)

// putCachedMsg is a no-op under -tags nocache. The default build caches
// via bounded chan; this build drops to GC so PERF-05.1 can measure the
// cache contribution to allocs/op.
func putCachedMsg(msg proto.Message) {
	_ = msg
}

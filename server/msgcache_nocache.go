//go:build nocache

// Package server — no-cache baseline used by Phase 13 benchmarking to produce
// the A/B comparison required by PERF-05.1 ("allocs/op ≥ 4 lower than a no-cache
// baseline"). Build with `go test -tags nocache` to select this implementation;
// the default build uses the bounded-chan cache in msgcache.go.
//
// Every getCachedX function returns a fresh allocation; putCachedMsg is a no-op.
// No cache chans are declared under this build — the cache state is simply absent.
//
// This file exists SOLELY to enable A/B bench comparison. It MUST NOT be
// shipped in production binaries; the default build (no -tags) excludes it.
package server

import (
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

func getCachedTread() *proto.Tread     { return &proto.Tread{} }
func getCachedTwrite() *proto.Twrite   { return &proto.Twrite{} }
func getCachedTwalk() *proto.Twalk     { return &proto.Twalk{} }
func getCachedTclunk() *proto.Tclunk   { return &proto.Tclunk{} }
func getCachedTlopen() *p9l.Tlopen     { return &p9l.Tlopen{} }
func getCachedTgetattr() *p9l.Tgetattr { return &p9l.Tgetattr{} }
func getCachedTlcreate() *p9l.Tlcreate { return &p9l.Tlcreate{} }

func putCachedMsg(msg proto.Message) {
	// No-op. The cache-on build returns structs to bounded chans here;
	// this build lets them drop to GC.
	_ = msg
}

package server

import (
	"hash/fnv"
	"sync/atomic"

	"github.com/dotwaffle/ninep/proto"
)

// QIDGenerator produces QIDs with monotonically increasing Path values.
// Safe for concurrent use.
type QIDGenerator struct {
	next atomic.Uint64
}

// Next returns a new QID with the given type and a unique path.
// Each call increments the internal counter atomically.
func (g *QIDGenerator) Next(t proto.QIDType) proto.QID {
	return proto.QID{
		Type: t,
		Path: g.next.Add(1),
	}
}

// PathQID returns a deterministic QID derived from the given path string
// using FNV-1a 64-bit hashing. Useful for nodes with stable, known paths.
func PathQID(t proto.QIDType, path string) proto.QID {
	h := fnv.New64a()
	// hash/fnv.Write never returns an error.
	_, _ = h.Write([]byte(path))
	return proto.QID{
		Type: t,
		Path: h.Sum64(),
	}
}

// nodeQID resolves the QID for a node using the following priority:
//  1. QIDer interface (node provides its own QID)
//  2. InodeEmbedder (use the embedded Inode's QID)
//  3. Node.QID() fallback (Phase 2 compatibility)
func nodeQID(node Node) proto.QID {
	if q, ok := node.(QIDer); ok {
		return q.QID()
	}
	if ie, ok := node.(InodeEmbedder); ok {
		return ie.EmbeddedInode().QID()
	}
	return node.QID()
}

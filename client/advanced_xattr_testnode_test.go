package client_test

import (
	"context"
	"sort"
	"sync"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

// testXattrNode is the Wave-2 xattr fixture used by the 21-03 high-level
// round-trips. It implements the four simple xattr capability interfaces
// (NodeXattrGetter/Setter/Lister/Remover) over a map-backed, mutex-
// guarded store.
//
// Disjoint namespace: Wave-1 plan 21-01 ships rawTestXattrNode (prefixed
// "raw") in raw_advanced_test.go for the wire-level Txattrwalk /
// Txattrcreate tests. This type uses the Wave-2 unprefixed "test" name
// so Wave-2 plans see a clean fixture without colliding with Wave-1's
// sharper-edged helpers.
type testXattrNode struct {
	server.Inode
	qid proto.QID

	mu     sync.Mutex
	xattrs map[string][]byte
}

var (
	_ server.NodeXattrGetter  = (*testXattrNode)(nil)
	_ server.NodeXattrSetter  = (*testXattrNode)(nil)
	_ server.NodeXattrLister  = (*testXattrNode)(nil)
	_ server.NodeXattrRemover = (*testXattrNode)(nil)
)

// newTestXattrNode constructs a testXattrNode seeded with the caller's
// xattrs map (deep-copied so subsequent caller mutations do not leak
// through). Uses gen.Next(QTFILE) for the QID; Init(qid, self) wires
// the Inode so the server-side tree honours QID + capability lookups.
func newTestXattrNode(gen *server.QIDGenerator, seed map[string][]byte) *testXattrNode {
	x := &testXattrNode{
		qid:    gen.Next(proto.QTFILE),
		xattrs: make(map[string][]byte, len(seed)),
	}
	for k, v := range seed {
		x.xattrs[k] = append([]byte(nil), v...)
	}
	x.Init(x.qid, x)
	return x
}

// QID satisfies the node QID-resolution path on the server bridge.
func (x *testXattrNode) QID() proto.QID { return x.qid }

// GetXattr returns a deep copy of the stored value, or proto.ENODATA
// (the server's errnoFromError treats proto.Errno as a sentinel and
// wires it into Rlerror.Ecode verbatim).
func (x *testXattrNode) GetXattr(_ context.Context, name string) ([]byte, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	v, ok := x.xattrs[name]
	if !ok {
		return nil, proto.ENODATA
	}
	return append([]byte(nil), v...), nil
}

// SetXattr stores a deep copy of data under name. flags are ignored --
// the fixture's contract is "last write wins" regardless of caller
// intent (XATTR_CREATE/REPLACE semantics are a server concern, not
// interesting for client-side round-trip testing).
func (x *testXattrNode) SetXattr(_ context.Context, name string, data []byte, _ uint32) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.xattrs == nil {
		x.xattrs = make(map[string][]byte)
	}
	x.xattrs[name] = append([]byte(nil), data...)
	return nil
}

// ListXattrs returns a sorted snapshot of the attribute names. Sorting
// at the fixture level means tests can compare against a fixed
// []string literal rather than unpacking an arbitrary-order map scan.
func (x *testXattrNode) ListXattrs(_ context.Context) ([]string, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	names := make([]string, 0, len(x.xattrs))
	for k := range x.xattrs {
		names = append(names, k)
	}
	sort.Strings(names)
	return names, nil
}

// RemoveXattr deletes name from the map, returning proto.ENODATA if it
// was not present (matches Linux removexattr(2) semantics).
func (x *testXattrNode) RemoveXattr(_ context.Context, name string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if _, ok := x.xattrs[name]; !ok {
		return proto.ENODATA
	}
	delete(x.xattrs, name)
	return nil
}

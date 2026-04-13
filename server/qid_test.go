package server

import (
	"sync"
	"testing"

	"github.com/dotwaffle/ninep/proto"
)

func TestQIDGeneratorNext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		qidType   proto.QIDType
		calls     int
		wantPaths []uint64
	}{
		{
			name:      "monotonic file paths",
			qidType:   proto.QTFILE,
			calls:     3,
			wantPaths: []uint64{1, 2, 3},
		},
		{
			name:    "directory type preserved",
			qidType: proto.QTDIR,
			calls:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := &QIDGenerator{}
			for i := range tt.calls {
				qid := g.Next(tt.qidType)
				if qid.Type != tt.qidType {
					t.Errorf("call %d: Type = %d, want %d", i, qid.Type, tt.qidType)
				}
				if tt.wantPaths != nil && qid.Path != tt.wantPaths[i] {
					t.Errorf("call %d: Path = %d, want %d", i, qid.Path, tt.wantPaths[i])
				}
			}
		})
	}
}

func TestQIDGeneratorConcurrent(t *testing.T) {
	t.Parallel()

	g := &QIDGenerator{}
	const goroutines = 10
	const callsPerGoroutine = 100

	var seen sync.Map
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			for range callsPerGoroutine {
				qid := g.Next(proto.QTFILE)
				if _, loaded := seen.LoadOrStore(qid.Path, true); loaded {
					t.Errorf("duplicate path: %d", qid.Path)
				}
			}
		}()
	}

	wg.Wait()

	count := 0
	seen.Range(func(_, _ any) bool {
		count++
		return true
	})
	if count != goroutines*callsPerGoroutine {
		t.Errorf("unique paths = %d, want %d", count, goroutines*callsPerGoroutine)
	}
}

func TestPathQID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		qidType  proto.QIDType
		path     string
		wantSame string // if non-empty, check determinism against this path
		wantDiff string // if non-empty, check different QID from this path
	}{
		{
			name:     "deterministic file",
			qidType:  proto.QTFILE,
			path:     "/foo/bar",
			wantSame: "/foo/bar",
		},
		{
			name:     "deterministic dir",
			qidType:  proto.QTDIR,
			path:     "/root",
			wantSame: "/root",
		},
		{
			name:     "different paths produce different QIDs",
			qidType:  proto.QTFILE,
			path:     "/foo",
			wantDiff: "/bar",
		},
		{
			name:    "type preserved",
			qidType: proto.QTDIR,
			path:    "/somedir",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			qid := PathQID(tt.qidType, tt.path)
			if qid.Type != tt.qidType {
				t.Errorf("Type = %d, want %d", qid.Type, tt.qidType)
			}

			if tt.wantSame != "" {
				qid2 := PathQID(tt.qidType, tt.wantSame)
				if qid != qid2 {
					t.Errorf("PathQID(%q) = %v, PathQID(%q) = %v; want same", tt.path, qid, tt.wantSame, qid2)
				}
			}

			if tt.wantDiff != "" {
				qid2 := PathQID(tt.qidType, tt.wantDiff)
				if qid.Path == qid2.Path {
					t.Errorf("PathQID(%q).Path = PathQID(%q).Path = %d; want different", tt.path, tt.wantDiff, qid.Path)
				}
			}
		})
	}
}

func TestNodeQID(t *testing.T) {
	t.Parallel()

	wantQID := proto.QID{Type: proto.QTFILE, Version: 1, Path: 42}

	tests := []struct {
		name    string
		node    Node
		wantQID proto.QID
	}{
		{
			name:    "QIDer takes priority",
			node:    &testQIDerNode{qid: wantQID},
			wantQID: wantQID,
		},
		{
			name: "InodeEmbedder fallback",
			node: func() Node {
				n := &testInodeNode{}
				n.Init(wantQID, n)
				return n
			}(),
			wantQID: wantQID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := nodeQID(tt.node)
			if got != tt.wantQID {
				t.Errorf("nodeQID() = %v, want %v", got, tt.wantQID)
			}
		})
	}
}

// testQIDerNode implements both Node and QIDer.
type testQIDerNode struct {
	qid proto.QID
}

func (n *testQIDerNode) QID() proto.QID { return n.qid }

// testInodeNode embeds Inode and implements InodeEmbedder.
type testInodeNode struct {
	Inode
}

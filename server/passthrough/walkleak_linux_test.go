//go:build linux

package passthrough

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dotwaffle/ninep/proto"
)

// countFds returns the number of open file descriptors in this process.
func countFds(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("read /proc/self/fd: %v", err)
	}
	return len(ents)
}

// TestWalk_IntermediateNodesDoNotLeakFds is a regression test for the walk
// fd leak: every multi-component Twalk opened an fd per intermediate
// directory via Lookup and never closed it, growing the process fd table by
// one per walked component until exhaustion. Not parallel: it counts
// process-global fds.
func TestWalk_IntermediateNodesDoNotLeakFds(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "a", "b", "c"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "b", "c", "leaf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}

	cp := newConnPair(t, root)
	defer cp.close(t)
	attach(t, cp, 1, 0)

	walkClunk := func() {
		t.Helper()
		sendMessage(t, cp.client, 10, &proto.Twalk{
			Fid:    0,
			NewFid: 1,
			Names:  []string{"a", "b", "c", "leaf"},
		})
		_, msg := readResponse(t, cp.client)
		rw, ok := msg.(*proto.Rwalk)
		if !ok {
			t.Fatalf("expected Rwalk, got %T: %+v", msg, msg)
		}
		if len(rw.QIDs) != 4 {
			t.Fatalf("walk resolved %d of 4 components", len(rw.QIDs))
		}
		sendMessage(t, cp.client, 11, &proto.Tclunk{Fid: 1})
		if _, msg := readResponse(t, cp.client); msg == nil {
			t.Fatal("no Rclunk")
		}
	}

	// Warm up once so lazily created fds land in the baseline.
	walkClunk()
	base := countFds(t)

	const rounds = 50
	for range rounds {
		walkClunk()
	}

	if grown := countFds(t) - base; grown > 3 {
		t.Errorf("fd count grew by %d over %d walks (leaked ~%d fds/walk); intermediate walk nodes are not being closed",
			grown, rounds, grown/rounds)
	}
}

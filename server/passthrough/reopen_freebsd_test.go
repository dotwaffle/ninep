//go:build freebsd

package passthrough

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// TestOpenResolved_RejectsSymlinkSwap pins the FreeBSD reopen-by-name TOCTOU
// fix: openResolved falls back to unix.Openat(parentFd, name, ...) because
// FreeBSD has no /proc/self/fd equivalent to reopen an already-open fd
// directly (see reopen_linux.go). If the directory entry is replaced with a
// symlink between the original Lookup and this reopen, O_NOFOLLOW must
// reject it outright instead of silently following the symlink to whatever
// target an attacker chose.
func TestOpenResolved_RejectsSymlinkSwap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	targetPath := filepath.Join(dir, "target")
	if err := os.WriteFile(targetPath, []byte("original"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	victimPath := filepath.Join(dir, "victim")
	if err := os.WriteFile(victimPath, []byte("victim"), 0o600); err != nil {
		t.Fatalf("write victim: %v", err)
	}

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatalf("NewRoot(%q): %v", dir, err)
	}
	t.Cleanup(func() { _ = root.Close(t.Context()) })

	child, err := root.Lookup(context.Background(), "target")
	if err != nil {
		t.Fatalf("Lookup(target): %v", err)
	}
	n, ok := child.(*Node)
	if !ok {
		t.Fatalf("Lookup(target) returned %T, want *Node", child)
	}

	// Simulate n.fd having gone bad (the condition under which
	// chmodResolved/truncateResolved fall back to openResolved).
	_ = unix.Close(n.fd)
	n.fd = -1

	// Swap the directory entry for a symlink pointing at a different file.
	if err := os.Remove(targetPath); err != nil {
		t.Fatalf("remove target: %v", err)
	}
	if err := os.Symlink(victimPath, targetPath); err != nil {
		t.Fatalf("symlink target -> victim: %v", err)
	}

	if _, err := n.openResolved(unix.O_RDONLY); err != unix.ELOOP {
		t.Errorf("openResolved after symlink swap = %v, want ELOOP", err)
	}
}

// TestOpenResolved_RejectsRegularFileSwap covers the case O_NOFOLLOW alone
// cannot catch: the directory entry is removed and replaced with a NEW
// regular file (not a symlink) before the reopen. openResolved must still
// reject it, this time via the (dev, ino) identity check against the
// Node's original Lookup-time stat.
func TestOpenResolved_RejectsRegularFileSwap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	targetPath := filepath.Join(dir, "target")
	if err := os.WriteFile(targetPath, []byte("original"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatalf("NewRoot(%q): %v", dir, err)
	}
	t.Cleanup(func() { _ = root.Close(t.Context()) })

	child, err := root.Lookup(context.Background(), "target")
	if err != nil {
		t.Fatalf("Lookup(target): %v", err)
	}
	n, ok := child.(*Node)
	if !ok {
		t.Fatalf("Lookup(target) returned %T, want *Node", child)
	}

	_ = unix.Close(n.fd)
	n.fd = -1

	if err := os.Remove(targetPath); err != nil {
		t.Fatalf("remove target: %v", err)
	}
	if err := os.WriteFile(targetPath, []byte("different-inode"), 0o600); err != nil {
		t.Fatalf("recreate target: %v", err)
	}

	if _, err := n.openResolved(unix.O_RDONLY); err != unix.ESTALE {
		t.Errorf("openResolved after regular-file swap = %v, want ESTALE", err)
	}
}

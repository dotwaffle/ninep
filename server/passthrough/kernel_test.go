//go:build integration

package passthrough

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	"github.com/dotwaffle/ninep/server"
)

// mountV9FS starts a passthrough 9P server on a Unix socket and mounts it via
// Linux v9fs. It returns the mount point path. If mounting requires
// CAP_SYS_ADMIN and the caller lacks it, the test is skipped. The server,
// mount, and temp dirs are cleaned up via t.Cleanup.
func mountV9FS(t *testing.T, root *Root) string {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())

	sockDir := t.TempDir()
	sockPath := filepath.Join(sockDir, "9p.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		cancel()
		// Socket creation may fail in sandboxed environments (e.g. seccomp, AppArmor).
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			t.Skipf("unix socket creation not permitted: %v", err)
		}
		t.Fatalf("listen unix %s: %v", sockPath, err)
	}

	srv := server.New(root, server.WithMaxMsize(65536))

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ctx, ln)
	}()

	mountDir := filepath.Join(t.TempDir(), "mnt")
	if err := os.MkdirAll(mountDir, 0o755); err != nil {
		cancel()
		t.Fatalf("mkdir %s: %v", mountDir, err)
	}

	mountOpts := fmt.Sprintf("trans=unix,version=9p2000.L,msize=65536,uname=root")
	mounted := false

	// Try direct mount first.
	cmd := exec.Command("mount", "-t", "9p", "-o", mountOpts, sockPath, mountDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		// If we lack privileges, try unshare for unprivileged user namespace mount.
		if os.Getuid() != 0 {
			cmd = exec.Command("unshare", "--user", "--mount", "--map-root-user",
				"mount", "-t", "9p", "-o", mountOpts, sockPath, mountDir)
			if out2, err2 := cmd.CombinedOutput(); err2 != nil {
				cancel()
				t.Skipf("mount -t 9p failed (need root or CAP_SYS_ADMIN): %v\noutput: %s\nunshare output: %s", err, out, out2)
			}
			mounted = true
		} else {
			cancel()
			t.Skipf("mount -t 9p failed: %v\noutput: %s", err, out)
		}
	} else {
		mounted = true
	}

	if !mounted {
		cancel()
		t.Skip("could not mount 9p filesystem")
	}

	t.Cleanup(func() {
		// Unmount first.
		umount := exec.Command("umount", "-l", mountDir)
		umount.Run() // best-effort

		// Cancel server context and drain error.
		cancel()
		<-errCh
	})

	return mountDir
}

func TestKernelMountReadFile(t *testing.T) {
	t.Parallel()

	hostDir := t.TempDir()
	content := []byte("hello from kernel")
	if err := os.WriteFile(filepath.Join(hostDir, "hello.txt"), content, 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	root, err := NewRoot(hostDir)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	t.Cleanup(func() { root.Close(t.Context()) })

	mnt := mountV9FS(t, root)

	data, err := os.ReadFile(filepath.Join(mnt, "hello.txt"))
	if err != nil {
		t.Fatalf("ReadFile via mount: %v", err)
	}
	if string(data) != string(content) {
		t.Errorf("read via mount = %q, want %q", data, content)
	}
}

func TestKernelMountWriteFile(t *testing.T) {
	t.Parallel()

	hostDir := t.TempDir()

	root, err := NewRoot(hostDir)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	t.Cleanup(func() { root.Close(t.Context()) })

	mnt := mountV9FS(t, root)

	content := []byte("written via kernel")
	if err := os.WriteFile(filepath.Join(mnt, "newfile.txt"), content, 0o644); err != nil {
		t.Fatalf("WriteFile via mount: %v", err)
	}

	// Verify on host side.
	data, err := os.ReadFile(filepath.Join(hostDir, "newfile.txt"))
	if err != nil {
		t.Fatalf("ReadFile on host: %v", err)
	}
	if string(data) != string(content) {
		t.Errorf("host read = %q, want %q", data, content)
	}
}

func TestKernelMountReaddir(t *testing.T) {
	t.Parallel()

	hostDir := t.TempDir()

	// Create known directory structure.
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(hostDir, name), []byte(name), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(hostDir, "subdir"), 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	root, err := NewRoot(hostDir)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	t.Cleanup(func() { root.Close(t.Context()) })

	mnt := mountV9FS(t, root)

	entries, err := os.ReadDir(mnt)
	if err != nil {
		t.Fatalf("ReadDir via mount: %v", err)
	}

	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)

	want := []string{"a.txt", "b.txt", "subdir"}
	if len(names) != len(want) {
		t.Fatalf("ReadDir got %d entries %v, want %d entries %v", len(names), names, len(want), want)
	}
	for i, name := range names {
		if name != want[i] {
			t.Errorf("entry[%d] = %q, want %q", i, name, want[i])
		}
	}
}

func TestKernelMountStat(t *testing.T) {
	t.Parallel()

	hostDir := t.TempDir()

	content := []byte("stat test content of known size")
	if err := os.WriteFile(filepath.Join(hostDir, "file.txt"), content, 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	root, err := NewRoot(hostDir)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	t.Cleanup(func() { root.Close(t.Context()) })

	mnt := mountV9FS(t, root)

	info, err := os.Stat(filepath.Join(mnt, "file.txt"))
	if err != nil {
		t.Fatalf("Stat via mount: %v", err)
	}
	if info.Size() != int64(len(content)) {
		t.Errorf("Size = %d, want %d", info.Size(), len(content))
	}
	if info.Mode().Perm()&0o644 != 0o644 {
		t.Errorf("Mode = %o, want at least %o", info.Mode().Perm(), 0o644)
	}
}

func TestKernelMountCreateFile(t *testing.T) {
	t.Parallel()

	hostDir := t.TempDir()

	root, err := NewRoot(hostDir)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	t.Cleanup(func() { root.Close(t.Context()) })

	mnt := mountV9FS(t, root)

	content := []byte("created via kernel mount")
	if err := os.WriteFile(filepath.Join(mnt, "created.txt"), content, 0o644); err != nil {
		t.Fatalf("WriteFile via mount: %v", err)
	}

	// Verify the file exists on the host filesystem.
	data, err := os.ReadFile(filepath.Join(hostDir, "created.txt"))
	if err != nil {
		t.Fatalf("ReadFile on host: %v", err)
	}
	if string(data) != string(content) {
		t.Errorf("host read = %q, want %q", data, content)
	}
}

func TestKernelMountSkipGracefully(t *testing.T) {
	// Verify the mount helper skips gracefully without root.
	// If running as root, this test verifies mount succeeds.
	// This test is intentionally not parallel since it validates the skip path.

	hostDir := t.TempDir()

	root, err := NewRoot(hostDir)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	t.Cleanup(func() { root.Close(t.Context()) })

	// Try to detect if we can mount at all. If mount fails, the mountV9FS
	// helper will call t.Skip, which is exactly the graceful behavior we want.
	mnt := mountV9FS(t, root)

	// If we got here, mount succeeded. Do a basic check.
	if _, err := os.ReadDir(mnt); err != nil {
		t.Errorf("ReadDir on successful mount: %v", err)
	}

	// Check errno types propagate correctly through v9fs.
	_, err = os.Stat(filepath.Join(mnt, "nonexistent"))
	if err == nil {
		t.Error("Stat(nonexistent) should fail")
	}
	if pathErr, ok := errors.AsType[*os.PathError](err); ok {
		if !errors.Is(pathErr.Err, syscall.ENOENT) {
			t.Errorf("Stat(nonexistent) errno = %v, want ENOENT", pathErr.Err)
		}
	}
}

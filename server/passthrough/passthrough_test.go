package passthrough

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
	"github.com/dotwaffle/ninep/server"
)

func TestNewRoot_Success(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatalf("NewRoot(%q): %v", dir, err)
	}
	t.Cleanup(func() { _ = root.Close(context.Background()) })

	qid := root.QID()
	if qid.Type != proto.QTDIR {
		t.Errorf("QID.Type = %d, want QTDIR (%d)", qid.Type, proto.QTDIR)
	}
	// Path should be the real inode number from stat.
	var st syscall.Stat_t
	if err := syscall.Stat(dir, &st); err != nil {
		t.Fatalf("stat %q: %v", dir, err)
	}
	if qid.Path != st.Ino {
		t.Errorf("QID.Path = %d, want inode %d", qid.Path, st.Ino)
	}
}

func TestNewRoot_Nonexistent(t *testing.T) {
	t.Parallel()
	_, err := NewRoot("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Fatal("NewRoot with nonexistent path should return error")
	}
}

func TestStatToAttr(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("hello world"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	var st syscall.Stat_t
	if err := syscall.Stat(f.Name(), &st); err != nil {
		t.Fatalf("stat: %v", err)
	}

	mapper := IdentityMapper()
	attr := statToAttr(&st, mapper)

	if attr.Valid != proto.AttrAll {
		t.Errorf("Valid = %x, want AttrAll (%x)", attr.Valid, proto.AttrAll)
	}
	if attr.Mode != st.Mode {
		t.Errorf("Mode = %o, want %o", attr.Mode, st.Mode)
	}
	if attr.UID != st.Uid {
		t.Errorf("UID = %d, want %d", attr.UID, st.Uid)
	}
	if attr.GID != st.Gid {
		t.Errorf("GID = %d, want %d", attr.GID, st.Gid)
	}
	if attr.NLink != st.Nlink {
		t.Errorf("NLink = %d, want %d", attr.NLink, st.Nlink)
	}
	if attr.Size != uint64(st.Size) {
		t.Errorf("Size = %d, want %d", attr.Size, st.Size)
	}
	if attr.BlkSize != uint64(st.Blksize) {
		t.Errorf("BlkSize = %d, want %d", attr.BlkSize, st.Blksize)
	}
	if attr.Blocks != uint64(st.Blocks) {
		t.Errorf("Blocks = %d, want %d", attr.Blocks, st.Blocks)
	}
	if attr.ATimeSec != uint64(st.Atim.Sec) {
		t.Errorf("ATimeSec = %d, want %d", attr.ATimeSec, st.Atim.Sec)
	}
	if attr.ATimeNSec != uint64(st.Atim.Nsec) {
		t.Errorf("ATimeNSec = %d, want %d", attr.ATimeNSec, st.Atim.Nsec)
	}
	if attr.MTimeSec != uint64(st.Mtim.Sec) {
		t.Errorf("MTimeSec = %d, want %d", attr.MTimeSec, st.Mtim.Sec)
	}
	if attr.MTimeNSec != uint64(st.Mtim.Nsec) {
		t.Errorf("MTimeNSec = %d, want %d", attr.MTimeNSec, st.Mtim.Nsec)
	}
	if attr.CTimeSec != uint64(st.Ctim.Sec) {
		t.Errorf("CTimeSec = %d, want %d", attr.CTimeSec, st.Ctim.Sec)
	}
	if attr.CTimeNSec != uint64(st.Ctim.Nsec) {
		t.Errorf("CTimeNSec = %d, want %d", attr.CTimeNSec, st.Ctim.Nsec)
	}
}

func TestStatToQID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mode     uint32
		wantType proto.QIDType
	}{
		{"regular_file", syscall.S_IFREG | 0644, proto.QTFILE},
		{"directory", syscall.S_IFDIR | 0755, proto.QTDIR},
		{"symlink", syscall.S_IFLNK | 0777, proto.QTSYMLINK},
		{"block_device", syscall.S_IFBLK | 0660, proto.QTFILE},
		{"char_device", syscall.S_IFCHR | 0660, proto.QTFILE},
		{"fifo", syscall.S_IFIFO | 0644, proto.QTFILE},
		{"socket", syscall.S_IFSOCK | 0755, proto.QTFILE},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			st := &syscall.Stat_t{
				Mode: tt.mode,
				Ino:  12345,
				Ctim: syscall.Timespec{Sec: 1000},
			}
			qid := statToQID(st)
			if qid.Type != tt.wantType {
				t.Errorf("Type = %d, want %d", qid.Type, tt.wantType)
			}
			if qid.Path != 12345 {
				t.Errorf("Path = %d, want 12345", qid.Path)
			}
			if qid.Version != 1000 {
				t.Errorf("Version = %d, want 1000", qid.Version)
			}
		})
	}
}

func TestToProtoErr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want proto.Errno
	}{
		{"nil", nil, 0},
		{"ENOENT", syscall.ENOENT, proto.ENOENT},
		{"EPERM", syscall.EPERM, proto.EPERM},
		{"EACCES", syscall.EACCES, proto.EACCES},
		{"EEXIST", syscall.EEXIST, proto.EEXIST},
		{"ENOTDIR", syscall.ENOTDIR, proto.ENOTDIR},
		{"EISDIR", syscall.EISDIR, proto.EISDIR},
		{"ENOSYS", syscall.ENOSYS, proto.ENOSYS},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := toProtoErr(tt.err)
			if tt.err == nil {
				if got != nil {
					t.Errorf("toProtoErr(nil) = %v, want nil", got)
				}
				return
			}
			pErr, ok := got.(proto.Errno)
			if !ok {
				t.Fatalf("toProtoErr returned %T, want proto.Errno", got)
			}
			if pErr != tt.want {
				t.Errorf("toProtoErr(%v) = %d, want %d", tt.err, pErr, tt.want)
			}
		})
	}
}

func TestToProtoErr_UnknownError(t *testing.T) {
	t.Parallel()
	got := toProtoErr(os.ErrInvalid)
	pErr, ok := got.(proto.Errno)
	if !ok {
		t.Fatalf("toProtoErr returned %T, want proto.Errno", got)
	}
	if pErr != proto.EIO {
		t.Errorf("toProtoErr(unknown) = %d, want EIO (%d)", pErr, proto.EIO)
	}
}

func TestIdentityMapper(t *testing.T) {
	t.Parallel()
	m := IdentityMapper()

	uid, gid := m.ToHost(1000, 1000)
	if uid != 1000 || gid != 1000 {
		t.Errorf("ToHost(1000,1000) = (%d,%d), want (1000,1000)", uid, gid)
	}

	uid, gid = m.FromHost(500, 600)
	if uid != 500 || gid != 600 {
		t.Errorf("FromHost(500,600) = (%d,%d), want (500,600)", uid, gid)
	}
}

func TestCustomUIDMapper(t *testing.T) {
	t.Parallel()
	m := UIDMapper{
		ToHost: func(uid, gid uint32) (uint32, uint32) {
			return uid + 1000, gid + 1000
		},
		FromHost: func(uid, gid uint32) (uint32, uint32) {
			return uid - 1000, gid - 1000
		},
	}

	uid, gid := m.ToHost(100, 200)
	if uid != 1100 || gid != 1200 {
		t.Errorf("ToHost(100,200) = (%d,%d), want (1100,1200)", uid, gid)
	}

	uid, gid = m.FromHost(1100, 1200)
	if uid != 100 || gid != 200 {
		t.Errorf("FromHost(1100,1200) = (%d,%d), want (100,200)", uid, gid)
	}
}

func TestFileHandle_ReadWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "testfile")

	// Create file with content.
	if err := os.WriteFile(path, []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}

	fd, err := syscall.Open(path, syscall.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	fh := &fileHandle{fd: fd}
	ctx := context.Background()

	// Read from offset 0.
	data, err := fh.Read(ctx, 0, 5)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("Read(0,5) = %q, want %q", data, "hello")
	}

	// Read from offset 6.
	data, err = fh.Read(ctx, 6, 5)
	if err != nil {
		t.Fatalf("Read(6,5): %v", err)
	}
	if string(data) != "world" {
		t.Errorf("Read(6,5) = %q, want %q", data, "world")
	}

	// Write at offset.
	n, err := fh.Write(ctx, []byte("EARTH"), 6)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 5 {
		t.Errorf("Write count = %d, want 5", n)
	}

	// Read back written data.
	data, err = fh.Read(ctx, 6, 5)
	if err != nil {
		t.Fatalf("Read after write: %v", err)
	}
	if string(data) != "EARTH" {
		t.Errorf("Read after write = %q, want %q", data, "EARTH")
	}

	// Release.
	if err := fh.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// After release, Read should fail.
	_, err = fh.Read(ctx, 0, 5)
	if err == nil {
		t.Error("Read after Release should fail")
	}
}

func TestRoot_Getattr(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close(context.Background()) })

	ctx := context.Background()
	attr, err := root.Getattr(ctx, proto.AttrAll)
	if err != nil {
		t.Fatalf("Getattr: %v", err)
	}

	var st syscall.Stat_t
	if err := syscall.Stat(dir, &st); err != nil {
		t.Fatal(err)
	}

	if attr.Mode != st.Mode {
		t.Errorf("Mode = %o, want %o", attr.Mode, st.Mode)
	}
	if attr.UID != st.Uid {
		t.Errorf("UID = %d, want %d", attr.UID, st.Uid)
	}
	if attr.GID != st.Gid {
		t.Errorf("GID = %d, want %d", attr.GID, st.Gid)
	}
	if attr.Size != uint64(st.Size) {
		t.Errorf("Size = %d, want %d", attr.Size, st.Size)
	}
}

func TestRoot_Setattr_Mode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "testfile")
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	// Open the file as a Node for setattr.
	fd, err := syscall.Open(path, syscall.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close(context.Background()) })

	node := &Node{fd: fd, root: root}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		t.Fatal(err)
	}
	node.Init(statToQID(&st), node)

	ctx := context.Background()
	err = node.Setattr(ctx, proto.SetAttr{
		Valid: proto.SetAttrMode,
		Mode:  0600,
	})
	if err != nil {
		t.Fatalf("Setattr(Mode): %v", err)
	}

	// Verify.
	var st2 syscall.Stat_t
	if err := syscall.Stat(path, &st2); err != nil {
		t.Fatal(err)
	}
	if st2.Mode&0777 != 0600 {
		t.Errorf("mode after Setattr = %o, want 0600", st2.Mode&0777)
	}

	_ = syscall.Close(fd)
}

func TestRoot_Setattr_Size(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "testfile")
	if err := os.WriteFile(path, []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}

	fd, err := syscall.Open(path, syscall.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close(context.Background()) })

	node := &Node{fd: fd, root: root}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		t.Fatal(err)
	}
	node.Init(statToQID(&st), node)

	ctx := context.Background()
	err = node.Setattr(ctx, proto.SetAttr{
		Valid: proto.SetAttrSize,
		Size:  5,
	})
	if err != nil {
		t.Fatalf("Setattr(Size): %v", err)
	}

	// Verify.
	var st2 syscall.Stat_t
	if err := syscall.Stat(path, &st2); err != nil {
		t.Fatal(err)
	}
	if st2.Size != 5 {
		t.Errorf("size after Setattr = %d, want 5", st2.Size)
	}

	_ = syscall.Close(fd)
}

func TestRoot_Close(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := root.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// After Close, Getattr should fail (fd closed).
	_, err = root.Getattr(ctx, proto.AttrAll)
	if err == nil {
		t.Error("Getattr after Close should fail")
	}
}

func TestDirentType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode uint32
		want uint8
	}{
		{"regular", syscall.S_IFREG, 8},
		{"directory", syscall.S_IFDIR, 4},
		{"symlink", syscall.S_IFLNK, 10},
		{"block", syscall.S_IFBLK, 6},
		{"char", syscall.S_IFCHR, 2},
		{"fifo", syscall.S_IFIFO, 1},
		{"socket", syscall.S_IFSOCK, 12},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := direntType(tt.mode)
			if got != tt.want {
				t.Errorf("direntType(%#x) = %d, want %d", tt.mode, got, tt.want)
			}
		})
	}
}

// --- Task 2: Directory operations tests ---

func TestLookup_ExistingChild(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "child.txt"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close(context.Background()) })

	ctx := context.Background()
	child, err := root.Lookup(ctx, "child.txt")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if child.QID().Type != proto.QTFILE {
		t.Errorf("child QID.Type = %d, want QTFILE", child.QID().Type)
	}
}

func TestLookup_Nonexistent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close(context.Background()) })

	_, err = root.Lookup(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("Lookup of nonexistent should return error")
	}
	pErr, ok := err.(proto.Errno)
	if !ok {
		t.Fatalf("error type = %T, want proto.Errno", err)
	}
	if pErr != proto.ENOENT {
		t.Errorf("errno = %d, want ENOENT (%d)", pErr, proto.ENOENT)
	}
}

func TestCreate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close(context.Background()) })

	ctx := context.Background()
	child, fh, _, err := root.Create(ctx, "newfile.txt", syscall.O_RDWR, 0644, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if child.QID().Type != proto.QTFILE {
		t.Errorf("child QID.Type = %d, want QTFILE", child.QID().Type)
	}

	// Write via handle.
	writer := fh.(server.FileWriter)
	n, err := writer.Write(ctx, []byte("hello"), 0)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 5 {
		t.Errorf("Write count = %d, want 5", n)
	}

	// Release handle.
	releaser := fh.(server.FileReleaser)
	if err := releaser.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// Verify file exists.
	data, err := os.ReadFile(filepath.Join(dir, "newfile.txt"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("file contents = %q, want %q", data, "hello")
	}
}

func TestMkdir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close(context.Background()) })

	ctx := context.Background()
	child, err := root.Mkdir(ctx, "subdir", 0755, 0)
	if err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if child.QID().Type != proto.QTDIR {
		t.Errorf("child QID.Type = %d, want QTDIR", child.QID().Type)
	}

	// Verify on disk.
	fi, err := os.Stat(filepath.Join(dir, "subdir"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !fi.IsDir() {
		t.Error("subdir should be a directory")
	}
}

func TestSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "target.txt"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close(context.Background()) })

	ctx := context.Background()
	child, err := root.Symlink(ctx, "link", "target.txt", 0)
	if err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if child.QID().Type != proto.QTSYMLINK {
		t.Errorf("child QID.Type = %d, want QTSYMLINK", child.QID().Type)
	}

	// Verify readlink.
	target, err := child.(*Node).Readlink(ctx)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if target != "target.txt" {
		t.Errorf("Readlink = %q, want %q", target, "target.txt")
	}
}

func TestUnlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "todelete.txt")
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close(context.Background()) })

	ctx := context.Background()
	if err := root.Unlink(ctx, "todelete.txt", 0); err != nil {
		t.Fatalf("Unlink: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file should not exist after Unlink")
	}
}

func TestRename(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close(context.Background()) })

	ctx := context.Background()
	if err := root.Rename(ctx, "old.txt", root, "new.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "old.txt")); !os.IsNotExist(err) {
		t.Error("old.txt should not exist after Rename")
	}
	data, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "data" {
		t.Errorf("new.txt contents = %q, want %q", data, "data")
	}
}

func TestReaddir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Create test entries.
	if err := os.WriteFile(filepath.Join(dir, "file1.txt"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file1.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close(context.Background()) })

	ctx := context.Background()
	entries, err := root.Readdir(ctx)
	if err != nil {
		t.Fatalf("Readdir: %v", err)
	}

	if len(entries) != 3 {
		t.Fatalf("Readdir returned %d entries, want 3", len(entries))
	}

	// Collect names.
	names := make(map[string]bool)
	for _, e := range entries {
		names[e.Name] = true
	}
	for _, want := range []string{"file1.txt", "subdir", "link"} {
		if !names[want] {
			t.Errorf("missing entry %q", want)
		}
	}
}

func TestLock_NonBlocking(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "lockfile")
	if err := os.WriteFile(path, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	fd, err := syscall.Open(path, syscall.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(fd) }()

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close(context.Background()) })

	node := &Node{fd: fd, root: root}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		t.Fatal(err)
	}
	node.Init(statToQID(&st), node)

	ctx := context.Background()
	status, err := node.Lock(ctx, proto.LockTypeWrLck, 0, 0, 0, 1, "test")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if status != proto.LockStatusOK {
		t.Errorf("Lock status = %d, want OK (%d)", status, proto.LockStatusOK)
	}

	// Unlock.
	status, err = node.Lock(ctx, proto.LockTypeUnlck, 0, 0, 0, 1, "test")
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if status != proto.LockStatusOK {
		t.Errorf("Unlock status = %d, want OK (%d)", status, proto.LockStatusOK)
	}
}

func TestGetLock_NoConflict(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "lockfile")
	if err := os.WriteFile(path, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	fd, err := syscall.Open(path, syscall.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(fd) }()

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close(context.Background()) })

	node := &Node{fd: fd, root: root}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		t.Fatal(err)
	}
	node.Init(statToQID(&st), node)

	ctx := context.Background()
	lt, _, _, _, _, err := node.GetLock(ctx, proto.LockTypeWrLck, 0, 0, 1, "test")
	if err != nil {
		t.Fatalf("GetLock: %v", err)
	}
	if lt != proto.LockTypeUnlck {
		t.Errorf("GetLock type = %d, want Unlck (%d)", lt, proto.LockTypeUnlck)
	}
}

// --- Protocol-level end-to-end tests ---

// connPair creates a server serving the given root and returns the client-side
// connection. Replicates the pattern from server/walk_test.go for external
// package testing.
type connPair struct {
	client net.Conn
	done   chan struct{}
	cancel context.CancelFunc
}

func newConnPair(t *testing.T, root server.Node) *connPair {
	t.Helper()

	srv := server.New(root,
		server.WithMaxMsize(65536),
		server.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)

	client, srv2 := net.Pipe()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		_ = srv2.Close()
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.ServeConn(ctx, srv2)
	}()

	// Negotiate version.
	sendTversion(t, client, 65536, "9P2000.L")
	rv := readRversion(t, client)
	if rv.Version != "9P2000.L" {
		t.Fatalf("version negotiation failed: got %q", rv.Version)
	}

	return &connPair{client: client, done: done, cancel: cancel}
}

func (cp *connPair) close(t *testing.T) {
	t.Helper()
	_ = cp.client.Close()
	<-cp.done
	cp.cancel()
}

func sendTversion(t *testing.T, w io.Writer, msize uint32, version string) {
	t.Helper()
	var body bytes.Buffer
	tv := &proto.Tversion{Msize: msize, Version: version}
	if err := tv.EncodeTo(&body); err != nil {
		t.Fatalf("encode tversion body: %v", err)
	}
	size := uint32(proto.HeaderSize) + uint32(body.Len())
	if err := proto.WriteUint32(w, size); err != nil {
		t.Fatalf("write size: %v", err)
	}
	if err := proto.WriteUint8(w, uint8(proto.TypeTversion)); err != nil {
		t.Fatalf("write type: %v", err)
	}
	if err := proto.WriteUint16(w, uint16(proto.NoTag)); err != nil {
		t.Fatalf("write tag: %v", err)
	}
	if _, err := w.Write(body.Bytes()); err != nil {
		t.Fatalf("write body: %v", err)
	}
}

func readRversion(t *testing.T, r io.Reader) *proto.Rversion {
	t.Helper()
	size, err := proto.ReadUint32(r)
	if err != nil {
		t.Fatalf("read rversion size: %v", err)
	}
	if size < uint32(proto.HeaderSize) {
		t.Fatalf("rversion size too small: %d", size)
	}
	msgType, err := proto.ReadUint8(r)
	if err != nil {
		t.Fatalf("read rversion type: %v", err)
	}
	if proto.MessageType(msgType) != proto.TypeRversion {
		t.Fatalf("expected Rversion (type %d), got type %d", proto.TypeRversion, msgType)
	}
	if _, err := proto.ReadUint16(r); err != nil {
		t.Fatalf("read rversion tag: %v", err)
	}
	bodySize := int64(size) - int64(proto.HeaderSize)
	var rv proto.Rversion
	if err := rv.DecodeFrom(io.LimitReader(r, bodySize)); err != nil {
		t.Fatalf("decode rversion: %v", err)
	}
	return &rv
}

func sendMessage(t *testing.T, w io.Writer, tag proto.Tag, msg proto.Message) {
	t.Helper()
	if err := p9l.Encode(w, tag, msg); err != nil {
		t.Fatalf("encode %s: %v", msg.Type(), err)
	}
}

func readResponse(t *testing.T, r io.Reader) (proto.Tag, proto.Message) {
	t.Helper()
	tag, msg, err := p9l.Decode(r)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return tag, msg
}

func attach(t *testing.T, cp *connPair, tag proto.Tag, fid proto.Fid) *proto.Rattach {
	t.Helper()
	sendMessage(t, cp.client, tag, &proto.Tattach{
		Fid:   fid,
		Afid:  proto.NoFid,
		Uname: "user",
		Aname: "",
	})
	_, msg := readResponse(t, cp.client)
	ra, ok := msg.(*proto.Rattach)
	if !ok {
		t.Fatalf("expected Rattach, got %T: %+v", msg, msg)
	}
	return ra
}

func walk(t *testing.T, cp *connPair, tag proto.Tag, fid, newfid proto.Fid, names ...string) *proto.Rwalk {
	t.Helper()
	sendMessage(t, cp.client, tag, &proto.Twalk{
		Fid:    fid,
		NewFid: newfid,
		Names:  names,
	})
	_, msg := readResponse(t, cp.client)
	rw, ok := msg.(*proto.Rwalk)
	if !ok {
		t.Fatalf("expected Rwalk, got %T: %+v", msg, msg)
	}
	return rw
}

func clunk(t *testing.T, cp *connPair, tag proto.Tag, fid proto.Fid) {
	t.Helper()
	sendMessage(t, cp.client, tag, &proto.Tclunk{Fid: fid})
	_, msg := readResponse(t, cp.client)
	if _, ok := msg.(*proto.Rclunk); !ok {
		t.Fatalf("expected Rclunk, got %T: %+v", msg, msg)
	}
}

func TestProtocol_AttachWalkReadWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Create a test file.
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}

	cp := newConnPair(t, root)
	defer cp.close(t)

	// Attach.
	ra := attach(t, cp, 1, 0)
	if ra.QID.Type != proto.QTDIR {
		t.Errorf("attach QID.Type = %d, want QTDIR", ra.QID.Type)
	}

	// Walk to hello.txt.
	rw := walk(t, cp, 2, 0, 1, "hello.txt")
	if len(rw.QIDs) != 1 {
		t.Fatalf("walk QIDs = %d, want 1", len(rw.QIDs))
	}
	if rw.QIDs[0].Type != proto.QTFILE {
		t.Errorf("walk QID.Type = %d, want QTFILE", rw.QIDs[0].Type)
	}

	// Open.
	sendMessage(t, cp.client, 3, &p9l.Tlopen{Fid: 1, Flags: syscall.O_RDWR})
	_, msg := readResponse(t, cp.client)
	rl, ok := msg.(*p9l.Rlopen)
	if !ok {
		t.Fatalf("expected Rlopen, got %T: %+v", msg, msg)
	}
	if rl.QID.Type != proto.QTFILE {
		t.Errorf("open QID.Type = %d, want QTFILE", rl.QID.Type)
	}

	// Read.
	sendMessage(t, cp.client, 4, &proto.Tread{Fid: 1, Offset: 0, Count: 1024})
	_, msg = readResponse(t, cp.client)
	rr, ok := msg.(*proto.Rread)
	if !ok {
		t.Fatalf("expected Rread, got %T: %+v", msg, msg)
	}
	if string(rr.Data) != "hello world" {
		t.Errorf("Read = %q, want %q", rr.Data, "hello world")
	}

	// Write.
	sendMessage(t, cp.client, 5, &proto.Twrite{Fid: 1, Offset: 6, Data: []byte("EARTH")})
	_, msg = readResponse(t, cp.client)
	rwt, ok := msg.(*proto.Rwrite)
	if !ok {
		t.Fatalf("expected Rwrite, got %T: %+v", msg, msg)
	}
	if rwt.Count != 5 {
		t.Errorf("Write count = %d, want 5", rwt.Count)
	}

	// Read back.
	sendMessage(t, cp.client, 6, &proto.Tread{Fid: 1, Offset: 0, Count: 1024})
	_, msg = readResponse(t, cp.client)
	rr, ok = msg.(*proto.Rread)
	if !ok {
		t.Fatalf("expected Rread, got %T: %+v", msg, msg)
	}
	if string(rr.Data) != "hello EARTH" {
		t.Errorf("Read after write = %q, want %q", rr.Data, "hello EARTH")
	}

	// Clunk.
	clunk(t, cp, 7, 1)
}

func TestProtocol_Readdir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}

	cp := newConnPair(t, root)
	defer cp.close(t)

	// Attach and open root dir.
	attach(t, cp, 1, 0)

	sendMessage(t, cp.client, 2, &p9l.Tlopen{Fid: 0, Flags: syscall.O_RDONLY})
	_, msg := readResponse(t, cp.client)
	if _, ok := msg.(*p9l.Rlopen); !ok {
		t.Fatalf("expected Rlopen, got %T: %+v", msg, msg)
	}

	// Readdir.
	sendMessage(t, cp.client, 3, &p9l.Treaddir{Fid: 0, Offset: 0, Count: 65000})
	_, msg = readResponse(t, cp.client)
	rd, ok := msg.(*p9l.Rreaddir)
	if !ok {
		t.Fatalf("expected Rreaddir, got %T: %+v", msg, msg)
	}
	if len(rd.Data) == 0 {
		t.Fatal("readdir returned no data")
	}
}

func TestProtocol_CreateAndRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}

	cp := newConnPair(t, root)
	defer cp.close(t)

	attach(t, cp, 1, 0)

	// Create file.
	sendMessage(t, cp.client, 2, &p9l.Tlcreate{
		Fid:  0,
		Name: "created.txt",
		Flags: syscall.O_RDWR,
		Mode: 0644,
		GID:  0,
	})
	_, msg := readResponse(t, cp.client)
	rc, ok := msg.(*p9l.Rlcreate)
	if !ok {
		t.Fatalf("expected Rlcreate, got %T: %+v", msg, msg)
	}
	if rc.QID.Type != proto.QTFILE {
		t.Errorf("create QID.Type = %d, want QTFILE", rc.QID.Type)
	}

	// Write via the created fid (fid 0 is now the created file, per 9P spec).
	sendMessage(t, cp.client, 3, &proto.Twrite{Fid: 0, Offset: 0, Data: []byte("created!")})
	_, msg = readResponse(t, cp.client)
	if _, ok := msg.(*proto.Rwrite); !ok {
		t.Fatalf("expected Rwrite, got %T: %+v", msg, msg)
	}

	// Clunk the created fid.
	clunk(t, cp, 4, 0)

	// Verify file on disk.
	data, err := os.ReadFile(filepath.Join(dir, "created.txt"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "created!" {
		t.Errorf("file contents = %q, want %q", data, "created!")
	}
}

func TestProtocol_Mkdir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}

	cp := newConnPair(t, root)
	defer cp.close(t)

	attach(t, cp, 1, 0)

	// Mkdir.
	sendMessage(t, cp.client, 2, &p9l.Tmkdir{
		DirFid: 0,
		Name:   "newdir",
		Mode:   0755,
		GID:    0,
	})
	_, msg := readResponse(t, cp.client)
	rm, ok := msg.(*p9l.Rmkdir)
	if !ok {
		t.Fatalf("expected Rmkdir, got %T: %+v", msg, msg)
	}
	if rm.QID.Type != proto.QTDIR {
		t.Errorf("mkdir QID.Type = %d, want QTDIR", rm.QID.Type)
	}

	// Walk into it.
	rw := walk(t, cp, 3, 0, 1, "newdir")
	if len(rw.QIDs) != 1 {
		t.Fatalf("walk into newdir QIDs = %d, want 1", len(rw.QIDs))
	}
	if rw.QIDs[0].Type != proto.QTDIR {
		t.Errorf("walk QID.Type = %d, want QTDIR", rw.QIDs[0].Type)
	}

	clunk(t, cp, 4, 1)
}

func TestProtocol_UnlinkAt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "todelete.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}

	cp := newConnPair(t, root)
	defer cp.close(t)

	attach(t, cp, 1, 0)

	// Unlinkat.
	sendMessage(t, cp.client, 2, &p9l.Tunlinkat{
		DirFid: 0,
		Name:   "todelete.txt",
		Flags:  0,
	})
	_, msg := readResponse(t, cp.client)
	if _, ok := msg.(*p9l.Runlinkat); !ok {
		t.Fatalf("expected Runlinkat, got %T: %+v", msg, msg)
	}

	// Verify.
	if _, err := os.Stat(filepath.Join(dir, "todelete.txt")); !os.IsNotExist(err) {
		t.Error("file should be deleted after Unlinkat")
	}
}

func TestProtocol_Getattr(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}

	cp := newConnPair(t, root)
	defer cp.close(t)

	attach(t, cp, 1, 0)
	walk(t, cp, 2, 0, 1, "file.txt")

	// Getattr.
	sendMessage(t, cp.client, 3, &p9l.Tgetattr{Fid: 1, RequestMask: proto.AttrAll})
	_, msg := readResponse(t, cp.client)
	rga, ok := msg.(*p9l.Rgetattr)
	if !ok {
		t.Fatalf("expected Rgetattr, got %T: %+v", msg, msg)
	}

	if rga.Attr.Size != 5 {
		t.Errorf("Attr.Size = %d, want 5", rga.Attr.Size)
	}
	if rga.Attr.Mode&0777 != 0644 {
		t.Errorf("Attr.Mode = %o, want 0644", rga.Attr.Mode&0777)
	}

	clunk(t, cp, 4, 1)
}

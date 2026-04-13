package passthrough

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/dotwaffle/ninep/proto"
)

func TestNewRoot_Success(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	root, err := NewRoot(dir)
	if err != nil {
		t.Fatalf("NewRoot(%q): %v", dir, err)
	}
	t.Cleanup(func() { root.Close(context.Background()) })

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
	f.Close()

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
	t.Cleanup(func() { root.Close(context.Background()) })

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
	t.Cleanup(func() { root.Close(context.Background()) })

	node := &Node{fd: fd, root: root}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		t.Fatal(err)
	}
	node.Inode.Init(statToQID(&st), node)

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

	syscall.Close(fd)
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
	t.Cleanup(func() { root.Close(context.Background()) })

	node := &Node{fd: fd, root: root}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		t.Fatal(err)
	}
	node.Inode.Init(statToQID(&st), node)

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

	syscall.Close(fd)
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

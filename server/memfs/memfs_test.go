package memfs

import (
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

func newGen() *server.QIDGenerator {
	return &server.QIDGenerator{}
}

func TestNewFileCopiesInput(t *testing.T) {
	t.Parallel()
	input := []byte("same")
	f := NewFile(input)
	input[0] = 'X'
	if got := string(f.Snapshot()); got != "same" {
		t.Fatalf("Snapshot = %q, want %q", got, "same")
	}
}

// --- MemFile Tests ---

func TestMemFileRead(t *testing.T) {
	t.Parallel()
	gen := newGen()
	f := NewFile([]byte("hello world"))
	f.Init(gen.Next(proto.QTFILE), f)

	tests := []struct {
		name   string
		offset uint64
		count  uint32
		want   string
	}{
		{"full read", 0, 11, "hello world"},
		{"partial read", 0, 5, "hello"},
		{"offset read", 6, 5, "world"},
		{"offset partial", 6, 3, "wor"},
		{"count exceeds", 6, 100, "world"},
		{"past EOF", 20, 5, ""},
		{"at EOF boundary", 11, 5, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			buf := make([]byte, tt.count)
			n, err := f.Read(t.Context(), buf, tt.offset)
			if err != nil {
				t.Fatalf("Read(%d, %d) error: %v", tt.offset, tt.count, err)
			}
			if string(buf[:n]) != tt.want {
				t.Errorf("Read(%d, %d) = %q, want %q", tt.offset, tt.count, buf[:n], tt.want)
			}
		})
	}
}

func TestMemFileWrite(t *testing.T) {
	t.Parallel()
	gen := newGen()

	t.Run("overwrite", func(t *testing.T) {
		t.Parallel()
		f := NewFile([]byte("hello"))
		f.Init(gen.Next(proto.QTFILE), f)

		n, err := f.Write(t.Context(), []byte("world"), 0)
		if err != nil {
			t.Fatalf("Write error: %v", err)
		}
		if n != 5 {
			t.Errorf("Write returned %d, want 5", n)
		}
		if string(f.Snapshot()) != "world" {
			t.Errorf("Data = %q, want %q", f.Snapshot(), "world")
		}
	})

	t.Run("extend", func(t *testing.T) {
		t.Parallel()
		f := NewFile([]byte("hi"))
		f.Init(gen.Next(proto.QTFILE), f)

		n, err := f.Write(t.Context(), []byte("hello"), 0)
		if err != nil {
			t.Fatalf("Write error: %v", err)
		}
		if n != 5 {
			t.Errorf("Write returned %d, want 5", n)
		}
		if string(f.Snapshot()) != "hello" {
			t.Errorf("Data = %q, want %q", f.Snapshot(), "hello")
		}
	})

	t.Run("append", func(t *testing.T) {
		t.Parallel()
		f := NewFile([]byte("hello"))
		f.Init(gen.Next(proto.QTFILE), f)

		n, err := f.Write(t.Context(), []byte(" world"), 5)
		if err != nil {
			t.Fatalf("Write error: %v", err)
		}
		if n != 6 {
			t.Errorf("Write returned %d, want 6", n)
		}
		if string(f.Snapshot()) != "hello world" {
			t.Errorf("Data = %q, want %q", f.Snapshot(), "hello world")
		}
	})

	t.Run("write with gap", func(t *testing.T) {
		t.Parallel()
		f := NewFile([]byte("hi"))
		f.Init(gen.Next(proto.QTFILE), f)

		n, err := f.Write(t.Context(), []byte("!"), 5)
		if err != nil {
			t.Fatalf("Write error: %v", err)
		}
		if n != 1 {
			t.Errorf("Write returned %d, want 1", n)
		}
		if len(f.Snapshot()) != 6 {
			t.Errorf("Data len = %d, want 6", len(f.Snapshot()))
		}
		if f.Snapshot()[5] != '!' {
			t.Errorf("Data[5] = %d, want %d", f.Snapshot()[5], '!')
		}
	})
}

func TestMemFileWriteRejectsOversizedGrowth(t *testing.T) {
	t.Parallel()

	gen := newGen()
	f := NewFile([]byte("hi"))
	f.Init(gen.Next(proto.QTFILE), f)

	if _, err := f.Write(t.Context(), []byte("!"), uint64(proto.MaxDataSize)); !errors.Is(err, proto.EFBIG) {
		t.Fatalf("Write err = %v, want EFBIG", err)
	}
	if string(f.Snapshot()) != "hi" {
		t.Fatalf("Data = %q, want unchanged %q", f.Snapshot(), "hi")
	}
}

func TestMemFileWriteRejectsOffsetOverflow(t *testing.T) {
	t.Parallel()

	gen := newGen()
	f := NewFile([]byte("hi"))
	f.Init(gen.Next(proto.QTFILE), f)

	if _, err := f.Write(t.Context(), []byte("!"), ^uint64(0)); !errors.Is(err, proto.EFBIG) {
		t.Fatalf("Write err = %v, want EFBIG", err)
	}
	if string(f.Snapshot()) != "hi" {
		t.Fatalf("Data = %q, want unchanged %q", f.Snapshot(), "hi")
	}
}

func TestMemFileSetattrRejectsOversizedSize(t *testing.T) {
	t.Parallel()

	gen := newGen()
	f := NewFile([]byte("hi"))
	f.Init(gen.Next(proto.QTFILE), f)

	err := f.Setattr(t.Context(), proto.SetAttr{
		Valid: proto.SetAttrSize,
		Size:  uint64(proto.MaxDataSize) + 1,
	})
	if !errors.Is(err, proto.EFBIG) {
		t.Fatalf("Setattr err = %v, want EFBIG", err)
	}
	if string(f.Snapshot()) != "hi" {
		t.Fatalf("Data = %q, want unchanged %q", f.Snapshot(), "hi")
	}
}

func TestMemFileSetattrValidatesBeforeMutation(t *testing.T) {
	t.Parallel()
	f := NewFileWithMode([]byte("hi"), 0o600)
	err := f.Setattr(t.Context(), proto.SetAttr{
		Valid: proto.SetAttrMode | proto.SetAttrSize,
		Mode:  0o644,
		Size:  uint64(proto.MaxDataSize) + 1,
	})
	if !errors.Is(err, proto.EFBIG) {
		t.Fatalf("Setattr err = %v, want EFBIG", err)
	}
	attr, err := f.Getattr(t.Context(), proto.AttrAll)
	if err != nil {
		t.Fatal(err)
	}
	if attr.Mode != 0o600 {
		t.Fatalf("mode after rejected Setattr = %#o, want %#o", attr.Mode, 0o600)
	}
}

func TestMemDirUnlinkSemantics(t *testing.T) {
	t.Parallel()
	root := NewDir(newGen()).
		AddFile("file", nil).
		WithDir("empty", func(*MemDir) {}).
		WithDir("nonempty", func(d *MemDir) { d.AddFile("child", nil) })

	if err := root.Unlink(t.Context(), "missing", 0); !errors.Is(err, proto.ENOENT) {
		t.Errorf("unlink missing = %v, want ENOENT", err)
	}
	if err := root.Unlink(t.Context(), "file", 0x200); !errors.Is(err, proto.ENOTDIR) {
		t.Errorf("rmdir file = %v, want ENOTDIR", err)
	}
	if err := root.Unlink(t.Context(), "empty", 0); !errors.Is(err, proto.EISDIR) {
		t.Errorf("unlink directory = %v, want EISDIR", err)
	}
	if err := root.Unlink(t.Context(), "nonempty", 0x200); !errors.Is(err, proto.ENOTEMPTY) {
		t.Errorf("rmdir nonempty = %v, want ENOTEMPTY", err)
	}
	if err := root.Unlink(t.Context(), "empty", 0x200); err != nil {
		t.Errorf("rmdir empty: %v", err)
	}
	if err := root.Unlink(t.Context(), "file", 0); err != nil {
		t.Errorf("unlink file: %v", err)
	}
}

func TestMemFileGetattr(t *testing.T) {
	t.Parallel()
	gen := newGen()

	t.Run("default mode", func(t *testing.T) {
		t.Parallel()
		f := NewFile([]byte("test"))
		f.Init(gen.Next(proto.QTFILE), f)

		attr, err := f.Getattr(t.Context(), proto.AttrAll)
		if err != nil {
			t.Fatalf("Getattr error: %v", err)
		}
		if attr.Mode != 0o644 {
			t.Errorf("Mode = %#o, want %#o", attr.Mode, 0o644)
		}
		if attr.Size != 4 {
			t.Errorf("Size = %d, want 4", attr.Size)
		}
		if attr.NLink != 1 {
			t.Errorf("NLink = %d, want 1", attr.NLink)
		}
	})

	t.Run("custom mode", func(t *testing.T) {
		t.Parallel()
		f := NewFileWithMode([]byte("test"), 0o600)
		f.Init(gen.Next(proto.QTFILE), f)

		attr, err := f.Getattr(t.Context(), proto.AttrAll)
		if err != nil {
			t.Fatalf("Getattr error: %v", err)
		}
		if attr.Mode != 0o600 {
			t.Errorf("Mode = %#o, want %#o", attr.Mode, 0o600)
		}
	})
}

func TestMemFileOpen(t *testing.T) {
	t.Parallel()
	gen := newGen()
	f := NewFile(nil)
	f.Init(gen.Next(proto.QTFILE), f)

	fh, flags, err := f.Open(t.Context(), 0)
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	if fh != nil {
		t.Errorf("Open handle = %v, want nil", fh)
	}
	if flags != 0 {
		t.Errorf("Open flags = %d, want 0", flags)
	}
}

func TestMemFileConcurrent(t *testing.T) {
	t.Parallel()
	gen := newGen()
	f := NewFile(make([]byte, 100))
	f.Init(gen.Next(proto.QTFILE), f)

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			_, _ = f.Read(t.Context(), make([]byte, 10), uint64(i*10))
		}(i)
		go func(i int) {
			defer wg.Done()
			data := make([]byte, 10)
			for j := range data {
				data[j] = byte(i)
			}
			_, _ = f.Write(t.Context(), data, uint64(i*10))
		}(i)
	}
	wg.Wait()
}

// --- MemDir Tests ---

func TestMemDirReaddir(t *testing.T) {
	t.Parallel()
	gen := newGen()
	dir := &MemDir{gen: gen}
	dir.Init(gen.Next(proto.QTDIR), dir)

	// Add children.
	f1 := NewFile([]byte("a"))
	f1.Init(gen.Next(proto.QTFILE), f1)
	dir.AddChild("file1", f1.EmbeddedInode())

	f2 := NewFile([]byte("b"))
	f2.Init(gen.Next(proto.QTFILE), f2)
	dir.AddChild("file2", f2.EmbeddedInode())

	entries, err := dir.Readdir(t.Context())
	if err != nil {
		t.Fatalf("Readdir error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("Readdir returned %d entries, want 2", len(entries))
	}

	names := make(map[string]bool)
	for _, e := range entries {
		names[e.Name] = true
		if e.Type != proto.DT_REG {
			t.Errorf("entry %q Type = %d, want %d", e.Name, e.Type, proto.DT_REG)
		}
	}
	if !names["file1"] || !names["file2"] {
		t.Errorf("missing expected entries: got %v", names)
	}
}

func TestMemDirOpen(t *testing.T) {
	t.Parallel()
	gen := newGen()
	dir := &MemDir{gen: gen}
	dir.Init(gen.Next(proto.QTDIR), dir)

	fh, flags, err := dir.Open(t.Context(), 0)
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	if fh != nil {
		t.Errorf("Open handle = %v, want nil", fh)
	}
	if flags != 0 {
		t.Errorf("Open flags = %d, want 0", flags)
	}
}

func TestMemDirGetattr(t *testing.T) {
	t.Parallel()
	gen := newGen()
	dir := &MemDir{gen: gen}
	dir.Init(gen.Next(proto.QTDIR), dir)

	// Add one child.
	f := NewFile(nil)
	f.Init(gen.Next(proto.QTFILE), f)
	dir.AddChild("file", f.EmbeddedInode())

	attr, err := dir.Getattr(t.Context(), proto.AttrAll)
	if err != nil {
		t.Fatalf("Getattr error: %v", err)
	}
	// S_IFDIR = 0o040000 | 0o755.
	wantMode := uint32(0o040000 | 0o755)
	if attr.Mode != wantMode {
		t.Errorf("Mode = %#o, want %#o", attr.Mode, wantMode)
	}
	// NLink = 2 + 1 child.
	if attr.NLink != 3 {
		t.Errorf("NLink = %d, want 3", attr.NLink)
	}
}

func TestMemDirCreate(t *testing.T) {
	t.Parallel()
	gen := newGen()
	dir := &MemDir{gen: gen}
	dir.Init(gen.Next(proto.QTDIR), dir)

	node, fh, flags, err := dir.Create(t.Context(), "newfile", 0, 0o644, 0)
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if fh != nil {
		t.Errorf("Create handle = %v, want nil", fh)
	}
	if flags != 0 {
		t.Errorf("Create flags = %d, want 0", flags)
	}
	if node == nil {
		t.Fatal("Create returned nil node")
	}

	// Verify child is in tree.
	child, err := dir.Lookup(t.Context(), "newfile")
	if err != nil {
		t.Fatalf("Lookup after Create: %v", err)
	}
	if child != node {
		t.Error("Lookup returned different node than Create")
	}

	// A second Create of the same name must fail with EEXIST, regardless
	// of flags, and must not replace the existing child.
	dup, _, _, err := dir.Create(t.Context(), "newfile", 0, 0o644, 0)
	if !errors.Is(err, proto.EEXIST) {
		t.Errorf("duplicate Create err = %v, want proto.EEXIST", err)
	}
	if dup != nil {
		t.Errorf("duplicate Create node = %v, want nil", dup)
	}
	if again, err := dir.Lookup(t.Context(), "newfile"); err != nil || again != node {
		t.Errorf("after duplicate Create, Lookup = (%v, %v), want original node", again, err)
	}
}

func TestMemDirMkdir(t *testing.T) {
	t.Parallel()
	gen := newGen()
	dir := &MemDir{gen: gen}
	dir.Init(gen.Next(proto.QTDIR), dir)

	node, err := dir.Mkdir(t.Context(), "subdir", 0o755, 0)
	if err != nil {
		t.Fatalf("Mkdir error: %v", err)
	}
	if node == nil {
		t.Fatal("Mkdir returned nil node")
	}

	// Verify child is in tree.
	child, err := dir.Lookup(t.Context(), "subdir")
	if err != nil {
		t.Fatalf("Lookup after Mkdir: %v", err)
	}
	if child != node {
		t.Error("Lookup returned different node than Mkdir")
	}

	// Verify it's a directory QID.
	if ie, ok := node.(server.InodeEmbedder); ok {
		qid := ie.EmbeddedInode().QID()
		if qid.Type != proto.QTDIR {
			t.Errorf("QID Type = %d, want QTDIR (%d)", qid.Type, proto.QTDIR)
		}
	} else {
		t.Error("Mkdir node does not implement InodeEmbedder")
	}

	// A second Mkdir of the same name must fail with EEXIST and must not
	// replace the existing child.
	dup, err := dir.Mkdir(t.Context(), "subdir", 0o755, 0)
	if !errors.Is(err, proto.EEXIST) {
		t.Errorf("duplicate Mkdir err = %v, want proto.EEXIST", err)
	}
	if dup != nil {
		t.Errorf("duplicate Mkdir node = %v, want nil", dup)
	}
}

// --- StaticFile Tests ---

func TestStaticFileRead(t *testing.T) {
	t.Parallel()
	gen := newGen()

	tests := []struct {
		name    string
		content string
		offset  uint64
		count   uint32
		want    string
	}{
		{"full", "hello", 0, 5, "hello"},
		{"partial", "hello", 0, 3, "hel"},
		{"offset", "hello", 2, 3, "llo"},
		{"past EOF", "hello", 10, 5, ""},
		{"count exceeds", "hello", 3, 100, "lo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := NewStaticFile(tt.content)
			f.Init(gen.Next(proto.QTFILE), f)

			buf := make([]byte, tt.count)
			n, err := f.Read(t.Context(), buf, tt.offset)
			if err != nil {
				t.Fatalf("Read error: %v", err)
			}
			if string(buf[:n]) != tt.want {
				t.Errorf("Read(%d, %d) = %q, want %q", tt.offset, tt.count, buf[:n], tt.want)
			}
		})
	}
}

func TestStaticFileGetattr(t *testing.T) {
	t.Parallel()
	gen := newGen()

	t.Run("default mode", func(t *testing.T) {
		t.Parallel()
		f := NewStaticFile("test data")
		f.Init(gen.Next(proto.QTFILE), f)

		attr, err := f.Getattr(t.Context(), proto.AttrAll)
		if err != nil {
			t.Fatalf("Getattr error: %v", err)
		}
		if attr.Mode != 0o444 {
			t.Errorf("Mode = %#o, want %#o", attr.Mode, 0o444)
		}
		if attr.Size != 9 {
			t.Errorf("Size = %d, want 9", attr.Size)
		}
		if attr.NLink != 1 {
			t.Errorf("NLink = %d, want 1", attr.NLink)
		}
	})

	t.Run("custom mode", func(t *testing.T) {
		t.Parallel()
		f := NewStaticFileWithMode("x", 0o400)
		f.Init(gen.Next(proto.QTFILE), f)

		attr, err := f.Getattr(t.Context(), proto.AttrAll)
		if err != nil {
			t.Fatalf("Getattr error: %v", err)
		}
		if attr.Mode != 0o400 {
			t.Errorf("Mode = %#o, want %#o", attr.Mode, 0o400)
		}
	})
}

func TestStaticFileOpen(t *testing.T) {
	t.Parallel()
	gen := newGen()
	f := NewStaticFile("data")
	f.Init(gen.Next(proto.QTFILE), f)

	fh, flags, err := f.Open(t.Context(), 0)
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	if fh != nil {
		t.Errorf("Open handle = %v, want nil", fh)
	}
	if flags != 0 {
		t.Errorf("Open flags = %d, want 0", flags)
	}
}

func TestStaticFileWriteReturnsENOSYS(t *testing.T) {
	t.Parallel()
	gen := newGen()
	f := NewStaticFile("readonly")
	f.Init(gen.Next(proto.QTFILE), f)

	// StaticFile does not implement NodeWriter, so Write comes from Inode.
	_, err := f.Write(t.Context(), []byte("x"), 0)
	if err != proto.ENOSYS {
		t.Errorf("Write err = %v, want ENOSYS", err)
	}
}

// --- Builder Tests ---

func TestBuilderNewDir(t *testing.T) {
	t.Parallel()
	gen := newGen()
	root := NewDir(gen)

	if root == nil {
		t.Fatal("NewDir returned nil")
	}
	qid := root.EmbeddedInode().QID()
	if qid.Type != proto.QTDIR {
		t.Errorf("QID Type = %d, want QTDIR (%d)", qid.Type, proto.QTDIR)
	}
}

func TestBuilderAddFile(t *testing.T) {
	t.Parallel()
	gen := newGen()
	root := NewDir(gen)
	ret := root.AddFile("hello.txt", []byte("hello"))

	// Returns same dir for chaining.
	if ret != root {
		t.Error("AddFile did not return parent dir")
	}

	// Child exists and is readable.
	child, err := root.Lookup(t.Context(), "hello.txt")
	if err != nil {
		t.Fatalf("Lookup error: %v", err)
	}
	r, ok := child.(server.NodeReader)
	if !ok {
		t.Fatal("child does not implement NodeReader")
	}
	buf := make([]byte, 100)
	n, err := r.Read(t.Context(), buf, 0)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if string(buf[:n]) != "hello" {
		t.Errorf("Read = %q, want %q", buf[:n], "hello")
	}
}

func TestBuilderAddStaticFile(t *testing.T) {
	t.Parallel()
	gen := newGen()
	root := NewDir(gen)
	ret := root.AddStaticFile("readme.txt", "static content")

	if ret != root {
		t.Error("AddStaticFile did not return parent dir")
	}

	child, err := root.Lookup(t.Context(), "readme.txt")
	if err != nil {
		t.Fatalf("Lookup error: %v", err)
	}
	r, ok := child.(server.NodeReader)
	if !ok {
		t.Fatal("child does not implement NodeReader")
	}
	buf := make([]byte, 100)
	n, err := r.Read(t.Context(), buf, 0)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if string(buf[:n]) != "static content" {
		t.Errorf("Read = %q, want %q", buf[:n], "static content")
	}
}

func TestBuilderAddDir(t *testing.T) {
	t.Parallel()
	gen := newGen()
	root := NewDir(gen)
	ret := root.AddDir("subdir")

	// Returns parent for chaining (not the child).
	if ret != root {
		t.Error("AddDir did not return parent dir")
	}

	child, err := root.Lookup(t.Context(), "subdir")
	if err != nil {
		t.Fatalf("Lookup error: %v", err)
	}
	ie, ok := child.(server.InodeEmbedder)
	if !ok {
		t.Fatal("child does not implement InodeEmbedder")
	}
	qid := ie.EmbeddedInode().QID()
	if qid.Type != proto.QTDIR {
		t.Errorf("child QID Type = %d, want QTDIR (%d)", qid.Type, proto.QTDIR)
	}
}

func TestBuilderSubDir(t *testing.T) {
	t.Parallel()
	gen := newGen()
	root := NewDir(gen)
	root.AddDir("sub")

	sub := root.SubDir("sub")
	if sub == nil {
		t.Fatal("SubDir returned nil")
	}
	sub.AddFile("nested.txt", []byte("nested"))

	// Verify nested file is walkable.
	child, err := root.Lookup(t.Context(), "sub")
	if err != nil {
		t.Fatalf("Lookup sub: %v", err)
	}
	l, ok := child.(server.NodeLookuper)
	if !ok {
		t.Fatal("sub dir does not implement NodeLookuper")
	}
	nested, err := l.Lookup(t.Context(), "nested.txt")
	if err != nil {
		t.Fatalf("Lookup nested.txt: %v", err)
	}
	r, ok := nested.(server.NodeReader)
	if !ok {
		t.Fatal("nested file does not implement NodeReader")
	}
	buf := make([]byte, 100)
	n, err := r.Read(t.Context(), buf, 0)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if string(buf[:n]) != "nested" {
		t.Errorf("Read = %q, want %q", buf[:n], "nested")
	}
}

func TestBuilderSubDirPanicsOnMissing(t *testing.T) {
	t.Parallel()
	gen := newGen()
	root := NewDir(gen)

	defer func() {
		if r := recover(); r == nil {
			t.Error("SubDir on missing name did not panic")
		}
	}()
	root.SubDir("nonexistent")
}

func TestBuilderWithDir(t *testing.T) {
	t.Parallel()
	gen := newGen()
	root := NewDir(gen)
	ret := root.WithDir("sub", func(d *MemDir) {
		d.AddFile("inner.txt", []byte("inside"))
	})

	// Returns parent for chaining.
	if ret != root {
		t.Error("WithDir did not return parent dir")
	}

	// Verify nested construction.
	child, err := root.Lookup(t.Context(), "sub")
	if err != nil {
		t.Fatalf("Lookup sub: %v", err)
	}
	l, ok := child.(server.NodeLookuper)
	if !ok {
		t.Fatal("sub does not implement NodeLookuper")
	}
	inner, err := l.Lookup(t.Context(), "inner.txt")
	if err != nil {
		t.Fatalf("Lookup inner.txt: %v", err)
	}
	r, ok := inner.(server.NodeReader)
	if !ok {
		t.Fatal("inner does not implement NodeReader")
	}
	buf := make([]byte, 100)
	n, err := r.Read(t.Context(), buf, 0)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if string(buf[:n]) != "inside" {
		t.Errorf("Read = %q, want %q", buf[:n], "inside")
	}
}

func TestBuilderChaining(t *testing.T) {
	t.Parallel()
	gen := newGen()
	root := NewDir(gen).
		AddFile("a.txt", []byte("aaa")).
		AddFile("b.txt", []byte("bbb")).
		AddStaticFile("c.txt", "ccc").
		AddDir("sub")

	// Verify all children exist.
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "sub"} {
		_, err := root.Lookup(t.Context(), name)
		if err != nil {
			t.Errorf("Lookup %q: %v", name, err)
		}
	}
}

func TestBuilderFreshNodes(t *testing.T) {
	t.Parallel()
	gen := newGen()
	data := []byte("same")
	root := NewDir(gen).
		AddFile("file1", data).
		AddFile("file2", data)

	// Each AddFile creates a distinct node.
	child1, err := root.Lookup(t.Context(), "file1")
	if err != nil {
		t.Fatalf("Lookup file1: %v", err)
	}
	child2, err := root.Lookup(t.Context(), "file2")
	if err != nil {
		t.Fatalf("Lookup file2: %v", err)
	}
	ie1 := child1.(server.InodeEmbedder)
	ie2 := child2.(server.InodeEmbedder)
	if ie1.EmbeddedInode() == ie2.EmbeddedInode() {
		t.Error("file1 and file2 share the same Inode (node reuse detected)")
	}
	q1 := ie1.EmbeddedInode().QID()
	q2 := ie2.EmbeddedInode().QID()
	if q1.Path == q2.Path {
		t.Error("file1 and file2 have the same QID Path")
	}
}

func TestBuilderAddSymlink(t *testing.T) {
	t.Parallel()
	gen := newGen()
	root := NewDir(gen).
		AddSymlink("link", "/target/path")

	child, err := root.Lookup(t.Context(), "link")
	if err != nil {
		t.Fatalf("Lookup link: %v", err)
	}
	rl, ok := child.(server.NodeReadlinker)
	if !ok {
		t.Fatal("symlink does not implement NodeReadlinker")
	}
	target, err := rl.Readlink(t.Context())
	if err != nil {
		t.Fatalf("Readlink error: %v", err)
	}
	if target != "/target/path" {
		t.Errorf("Readlink = %q, want %q", target, "/target/path")
	}
}

func TestBuilderAddFileWithMode(t *testing.T) {
	t.Parallel()
	gen := newGen()
	root := NewDir(gen).
		AddFileWithMode("exec.sh", []byte("#!/bin/sh"), 0o755)

	child, err := root.Lookup(t.Context(), "exec.sh")
	if err != nil {
		t.Fatalf("Lookup error: %v", err)
	}
	g, ok := child.(server.NodeGetattrer)
	if !ok {
		t.Fatal("child does not implement NodeGetattrer")
	}
	attr, err := g.Getattr(t.Context(), proto.AttrAll)
	if err != nil {
		t.Fatalf("Getattr error: %v", err)
	}
	if attr.Mode != 0o755 {
		t.Errorf("Mode = %#o, want %#o", attr.Mode, 0o755)
	}
}

func TestBuilderTreeWalkability(t *testing.T) {
	t.Parallel()
	gen := newGen()
	root := NewDir(gen).
		AddFile("top.txt", []byte("top")).
		WithDir("level1", func(d *MemDir) {
			d.AddFile("mid.txt", []byte("mid")).
				WithDir("level2", func(d2 *MemDir) {
					d2.AddFile("bottom.txt", []byte("bottom"))
				})
		})

	// Walk root -> level1 -> level2 -> bottom.txt.
	ctx := t.Context()

	l1, err := root.Lookup(ctx, "level1")
	if err != nil {
		t.Fatalf("Lookup level1: %v", err)
	}
	l2, err := l1.(server.NodeLookuper).Lookup(ctx, "level2")
	if err != nil {
		t.Fatalf("Lookup level2: %v", err)
	}
	bottom, err := l2.(server.NodeLookuper).Lookup(ctx, "bottom.txt")
	if err != nil {
		t.Fatalf("Lookup bottom.txt: %v", err)
	}
	buf := make([]byte, 100)
	n, err := bottom.(server.NodeReader).Read(ctx, buf, 0)
	if err != nil {
		t.Fatalf("Read bottom.txt: %v", err)
	}
	if string(buf[:n]) != "bottom" {
		t.Errorf("Read = %q, want %q", buf[:n], "bottom")
	}
}

// TestCreateMkdir_ConcurrentSameName asserts exactly one of two concurrent
// creates of the same name wins and the other sees EEXIST. The old
// Lookup-then-AddChild sequence was a check-then-act race in which both
// could pass the existence check and the second silently replaced the
// first entry.
func TestCreateMkdir_ConcurrentSameName(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		kind string
		op   func(d *MemDir, name string) error
	}{
		{"create", func(d *MemDir, name string) error {
			_, _, _, err := d.Create(t.Context(), name, 0, 0o644, 0)
			return err
		}},
		{"mkdir", func(d *MemDir, name string) error {
			_, err := d.Mkdir(t.Context(), name, 0o755, 0)
			return err
		}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			t.Parallel()
			d := NewDir(newGen())

			const rounds = 200
			for i := range rounds {
				name := tc.kind + "-" + strconv.Itoa(i)
				errs := make([]error, 2)
				var wg sync.WaitGroup
				for g := range errs {
					wg.Go(func() { errs[g] = tc.op(d, name) })
				}
				wg.Wait()

				var wins, exists int
				for _, err := range errs {
					switch {
					case err == nil:
						wins++
					case errors.Is(err, proto.EEXIST):
						exists++
					default:
						t.Fatalf("round %d: unexpected error %v", i, err)
					}
				}
				if wins != 1 || exists != 1 {
					t.Fatalf("round %d: wins=%d eexist=%d, want exactly one of each", i, wins, exists)
				}
			}
		})
	}
}

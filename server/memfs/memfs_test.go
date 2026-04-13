package memfs

import (
	"context"
	"sync"
	"testing"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

func newGen() *server.QIDGenerator {
	return &server.QIDGenerator{}
}

// --- MemFile Tests ---

func TestMemFileRead(t *testing.T) {
	t.Parallel()
	gen := newGen()
	f := &MemFile{Data: []byte("hello world")}
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
			got, err := f.Read(context.Background(), tt.offset, tt.count)
			if err != nil {
				t.Fatalf("Read(%d, %d) error: %v", tt.offset, tt.count, err)
			}
			if string(got) != tt.want {
				t.Errorf("Read(%d, %d) = %q, want %q", tt.offset, tt.count, got, tt.want)
			}
		})
	}
}

func TestMemFileWrite(t *testing.T) {
	t.Parallel()
	gen := newGen()

	t.Run("overwrite", func(t *testing.T) {
		t.Parallel()
		f := &MemFile{Data: []byte("hello")}
		f.Init(gen.Next(proto.QTFILE), f)

		n, err := f.Write(context.Background(), []byte("world"), 0)
		if err != nil {
			t.Fatalf("Write error: %v", err)
		}
		if n != 5 {
			t.Errorf("Write returned %d, want 5", n)
		}
		if string(f.Data) != "world" {
			t.Errorf("Data = %q, want %q", f.Data, "world")
		}
	})

	t.Run("extend", func(t *testing.T) {
		t.Parallel()
		f := &MemFile{Data: []byte("hi")}
		f.Init(gen.Next(proto.QTFILE), f)

		n, err := f.Write(context.Background(), []byte("hello"), 0)
		if err != nil {
			t.Fatalf("Write error: %v", err)
		}
		if n != 5 {
			t.Errorf("Write returned %d, want 5", n)
		}
		if string(f.Data) != "hello" {
			t.Errorf("Data = %q, want %q", f.Data, "hello")
		}
	})

	t.Run("append", func(t *testing.T) {
		t.Parallel()
		f := &MemFile{Data: []byte("hello")}
		f.Init(gen.Next(proto.QTFILE), f)

		n, err := f.Write(context.Background(), []byte(" world"), 5)
		if err != nil {
			t.Fatalf("Write error: %v", err)
		}
		if n != 6 {
			t.Errorf("Write returned %d, want 6", n)
		}
		if string(f.Data) != "hello world" {
			t.Errorf("Data = %q, want %q", f.Data, "hello world")
		}
	})

	t.Run("write with gap", func(t *testing.T) {
		t.Parallel()
		f := &MemFile{Data: []byte("hi")}
		f.Init(gen.Next(proto.QTFILE), f)

		n, err := f.Write(context.Background(), []byte("!"), 5)
		if err != nil {
			t.Fatalf("Write error: %v", err)
		}
		if n != 1 {
			t.Errorf("Write returned %d, want 1", n)
		}
		if len(f.Data) != 6 {
			t.Errorf("Data len = %d, want 6", len(f.Data))
		}
		if f.Data[5] != '!' {
			t.Errorf("Data[5] = %d, want %d", f.Data[5], '!')
		}
	})
}

func TestMemFileGetattr(t *testing.T) {
	t.Parallel()
	gen := newGen()

	t.Run("default mode", func(t *testing.T) {
		t.Parallel()
		f := &MemFile{Data: []byte("test")}
		f.Init(gen.Next(proto.QTFILE), f)

		attr, err := f.Getattr(context.Background(), proto.AttrAll)
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
		f := &MemFile{Data: []byte("test"), Mode: 0o600}
		f.Init(gen.Next(proto.QTFILE), f)

		attr, err := f.Getattr(context.Background(), proto.AttrAll)
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
	f := &MemFile{}
	f.Init(gen.Next(proto.QTFILE), f)

	fh, flags, err := f.Open(context.Background(), 0)
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
	f := &MemFile{Data: make([]byte, 100)}
	f.Init(gen.Next(proto.QTFILE), f)

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			_, _ = f.Read(context.Background(), uint64(i*10), 10)
		}(i)
		go func(i int) {
			defer wg.Done()
			data := make([]byte, 10)
			for j := range data {
				data[j] = byte(i)
			}
			_, _ = f.Write(context.Background(), data, uint64(i*10))
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
	f1 := &MemFile{Data: []byte("a")}
	f1.Init(gen.Next(proto.QTFILE), f1)
	dir.AddChild("file1", f1.EmbeddedInode())

	f2 := &MemFile{Data: []byte("b")}
	f2.Init(gen.Next(proto.QTFILE), f2)
	dir.AddChild("file2", f2.EmbeddedInode())

	entries, err := dir.Readdir(context.Background())
	if err != nil {
		t.Fatalf("Readdir error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("Readdir returned %d entries, want 2", len(entries))
	}

	names := make(map[string]bool)
	for _, e := range entries {
		names[e.Name] = true
		if e.Type != uint8(proto.QTFILE) {
			t.Errorf("entry %q Type = %d, want %d", e.Name, e.Type, proto.QTFILE)
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

	fh, flags, err := dir.Open(context.Background(), 0)
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
	f := &MemFile{}
	f.Init(gen.Next(proto.QTFILE), f)
	dir.AddChild("file", f.EmbeddedInode())

	attr, err := dir.Getattr(context.Background(), proto.AttrAll)
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

	node, fh, flags, err := dir.Create(context.Background(), "newfile", 0, 0o644, 0)
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
	child, err := dir.Lookup(context.Background(), "newfile")
	if err != nil {
		t.Fatalf("Lookup after Create: %v", err)
	}
	if child != node {
		t.Error("Lookup returned different node than Create")
	}
}

func TestMemDirMkdir(t *testing.T) {
	t.Parallel()
	gen := newGen()
	dir := &MemDir{gen: gen}
	dir.Init(gen.Next(proto.QTDIR), dir)

	node, err := dir.Mkdir(context.Background(), "subdir", 0o755, 0)
	if err != nil {
		t.Fatalf("Mkdir error: %v", err)
	}
	if node == nil {
		t.Fatal("Mkdir returned nil node")
	}

	// Verify child is in tree.
	child, err := dir.Lookup(context.Background(), "subdir")
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
			f := &StaticFile{Content: tt.content}
			f.Init(gen.Next(proto.QTFILE), f)

			got, err := f.Read(context.Background(), tt.offset, tt.count)
			if err != nil {
				t.Fatalf("Read error: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("Read(%d, %d) = %q, want %q", tt.offset, tt.count, got, tt.want)
			}
		})
	}
}

func TestStaticFileGetattr(t *testing.T) {
	t.Parallel()
	gen := newGen()

	t.Run("default mode", func(t *testing.T) {
		t.Parallel()
		f := &StaticFile{Content: "test data"}
		f.Init(gen.Next(proto.QTFILE), f)

		attr, err := f.Getattr(context.Background(), proto.AttrAll)
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
		f := &StaticFile{Content: "x", Mode: 0o400}
		f.Init(gen.Next(proto.QTFILE), f)

		attr, err := f.Getattr(context.Background(), proto.AttrAll)
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
	f := &StaticFile{Content: "data"}
	f.Init(gen.Next(proto.QTFILE), f)

	fh, flags, err := f.Open(context.Background(), 0)
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
	f := &StaticFile{Content: "readonly"}
	f.Init(gen.Next(proto.QTFILE), f)

	// StaticFile does not implement NodeWriter, so Write comes from Inode.
	_, err := f.Write(context.Background(), []byte("x"), 0)
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
	child, err := root.Lookup(context.Background(), "hello.txt")
	if err != nil {
		t.Fatalf("Lookup error: %v", err)
	}
	r, ok := child.(server.NodeReader)
	if !ok {
		t.Fatal("child does not implement NodeReader")
	}
	data, err := r.Read(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("Read = %q, want %q", data, "hello")
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

	child, err := root.Lookup(context.Background(), "readme.txt")
	if err != nil {
		t.Fatalf("Lookup error: %v", err)
	}
	r, ok := child.(server.NodeReader)
	if !ok {
		t.Fatal("child does not implement NodeReader")
	}
	data, err := r.Read(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if string(data) != "static content" {
		t.Errorf("Read = %q, want %q", data, "static content")
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

	child, err := root.Lookup(context.Background(), "subdir")
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
	child, err := root.Lookup(context.Background(), "sub")
	if err != nil {
		t.Fatalf("Lookup sub: %v", err)
	}
	l, ok := child.(server.NodeLookuper)
	if !ok {
		t.Fatal("sub dir does not implement NodeLookuper")
	}
	nested, err := l.Lookup(context.Background(), "nested.txt")
	if err != nil {
		t.Fatalf("Lookup nested.txt: %v", err)
	}
	r, ok := nested.(server.NodeReader)
	if !ok {
		t.Fatal("nested file does not implement NodeReader")
	}
	data, err := r.Read(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if string(data) != "nested" {
		t.Errorf("Read = %q, want %q", data, "nested")
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
	child, err := root.Lookup(context.Background(), "sub")
	if err != nil {
		t.Fatalf("Lookup sub: %v", err)
	}
	l, ok := child.(server.NodeLookuper)
	if !ok {
		t.Fatal("sub does not implement NodeLookuper")
	}
	inner, err := l.Lookup(context.Background(), "inner.txt")
	if err != nil {
		t.Fatalf("Lookup inner.txt: %v", err)
	}
	r, ok := inner.(server.NodeReader)
	if !ok {
		t.Fatal("inner does not implement NodeReader")
	}
	data, err := r.Read(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if string(data) != "inside" {
		t.Errorf("Read = %q, want %q", data, "inside")
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
		_, err := root.Lookup(context.Background(), name)
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

	// Each AddFile creates a distinct node (Pitfall 8).
	child1, err := root.Lookup(context.Background(), "file1")
	if err != nil {
		t.Fatalf("Lookup file1: %v", err)
	}
	child2, err := root.Lookup(context.Background(), "file2")
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

	child, err := root.Lookup(context.Background(), "link")
	if err != nil {
		t.Fatalf("Lookup link: %v", err)
	}
	rl, ok := child.(server.NodeReadlinker)
	if !ok {
		t.Fatal("symlink does not implement NodeReadlinker")
	}
	target, err := rl.Readlink(context.Background())
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

	child, err := root.Lookup(context.Background(), "exec.sh")
	if err != nil {
		t.Fatalf("Lookup error: %v", err)
	}
	g, ok := child.(server.NodeGetattrer)
	if !ok {
		t.Fatal("child does not implement NodeGetattrer")
	}
	attr, err := g.Getattr(context.Background(), proto.AttrAll)
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
	ctx := context.Background()

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
	data, err := bottom.(server.NodeReader).Read(ctx, 0, 100)
	if err != nil {
		t.Fatalf("Read bottom.txt: %v", err)
	}
	if string(data) != "bottom" {
		t.Errorf("Read = %q, want %q", data, "bottom")
	}
}

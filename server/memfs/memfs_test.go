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
	_, err := f.Inode.Write(context.Background(), []byte("x"), 0)
	if err != proto.ENOSYS {
		t.Errorf("Write err = %v, want ENOSYS", err)
	}
}

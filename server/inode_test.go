package server

import (
	"errors"
	"testing"

	"github.com/dotwaffle/ninep/proto"
)

func TestInodeEmbeddedInode(t *testing.T) {
	t.Parallel()

	i := &Inode{}
	if got := i.EmbeddedInode(); got != i {
		t.Errorf("EmbeddedInode() = %p, want %p", got, i)
	}
}

func TestInodeENOSYSDefaults(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	i := &Inode{}

	tests := []struct {
		name string
		fn   func() error
	}{
		{
			name: "Open",
			fn: func() error {
				fh, flags, err := i.Open(ctx, 0)
				if fh != nil {
					t.Error("Open: FileHandle not nil")
				}
				if flags != 0 {
					t.Error("Open: flags not 0")
				}
				return err
			},
		},
		{
			name: "Read",
			fn: func() error {
				n, err := i.Read(ctx, make([]byte, 1024), 0)
				if n != 0 {
					t.Error("Read: n not 0")
				}
				return err
			},
		},
		{
			name: "Write",
			fn: func() error {
				n, err := i.Write(ctx, []byte("data"), 0)
				if n != 0 {
					t.Error("Write: count not 0")
				}
				return err
			},
		},
		{
			name: "Getattr",
			fn: func() error {
				attr, err := i.Getattr(ctx, proto.AttrAll)
				if attr != (proto.Attr{}) {
					t.Error("Getattr: attr not zero")
				}
				return err
			},
		},
		{
			name: "Setattr",
			fn: func() error {
				return i.Setattr(ctx, proto.SetAttr{})
			},
		},
		{
			name: "Readdir",
			fn: func() error {
				dirents, err := i.Readdir(ctx)
				if dirents != nil {
					t.Error("Readdir: dirents not nil")
				}
				return err
			},
		},
		{
			name: "Create",
			fn: func() error {
				node, fh, flags, err := i.Create(ctx, "test", 0, 0, 0)
				if node != nil {
					t.Error("Create: node not nil")
				}
				if fh != nil {
					t.Error("Create: FileHandle not nil")
				}
				if flags != 0 {
					t.Error("Create: flags not 0")
				}
				return err
			},
		},
		{
			name: "Mkdir",
			fn: func() error {
				node, err := i.Mkdir(ctx, "dir", 0, 0)
				if node != nil {
					t.Error("Mkdir: node not nil")
				}
				return err
			},
		},
		{
			name: "Symlink",
			fn: func() error {
				node, err := i.Symlink(ctx, "link", "/target", 0)
				if node != nil {
					t.Error("Symlink: node not nil")
				}
				return err
			},
		},
		{
			name: "Link",
			fn: func() error {
				return i.Link(ctx, nil, "hardlink")
			},
		},
		{
			name: "Mknod",
			fn: func() error {
				node, err := i.Mknod(ctx, "dev", 0, 0, 0, 0)
				if node != nil {
					t.Error("Mknod: node not nil")
				}
				return err
			},
		},
		{
			name: "Readlink",
			fn: func() error {
				target, err := i.Readlink(ctx)
				if target != "" {
					t.Error("Readlink: target not empty")
				}
				return err
			},
		},
		{
			name: "Unlink",
			fn: func() error {
				return i.Unlink(ctx, "file", 0)
			},
		},
		{
			name: "Rename",
			fn: func() error {
				return i.Rename(ctx, "old", nil, "new")
			},
		},
		{
			name: "StatFS",
			fn: func() error {
				stat, err := i.StatFS(ctx)
				if stat != (proto.FSStat{}) {
					t.Error("StatFS: stat not zero")
				}
				return err
			},
		},
		{
			name: "Fsync",
			fn: func() error {
				return i.Fsync(ctx)
			},
		},
		{
			name: "Lock",
			fn: func() error {
				status, err := i.Lock(ctx, 0, 0, 0, 0, 0, "")
				if status != 0 {
					t.Error("Lock: status not 0")
				}
				return err
			},
		},
		{
			name: "GetLock",
			fn: func() error {
				lt, start, length, procID, clientID, err := i.GetLock(ctx, 0, 0, 0, 0, "")
				if lt != 0 || start != 0 || length != 0 || procID != 0 || clientID != "" {
					t.Error("GetLock: non-zero return values")
				}
				return err
			},
		},
		{
			name: "GetXattr",
			fn: func() error {
				data, err := i.GetXattr(ctx, "user.test")
				if data != nil {
					t.Error("GetXattr: data not nil")
				}
				return err
			},
		},
		{
			name: "SetXattr",
			fn: func() error {
				return i.SetXattr(ctx, "user.test", []byte("val"), 0)
			},
		},
		{
			name: "ListXattrs",
			fn: func() error {
				names, err := i.ListXattrs(ctx)
				if names != nil {
					t.Error("ListXattrs: names not nil")
				}
				return err
			},
		},
		{
			name: "RemoveXattr",
			fn: func() error {
				return i.RemoveXattr(ctx, "user.test")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.fn()
			if !errors.Is(err, proto.ENOSYS) {
				t.Errorf("%s error = %v, want ENOSYS", tt.name, err)
			}
		})
	}
}

func TestInodeCloseNoOp(t *testing.T) {
	t.Parallel()

	i := &Inode{}
	if err := i.Close(t.Context()); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}

func TestInodeLookupNoChildren(t *testing.T) {
	t.Parallel()

	i := &Inode{}
	node, err := i.Lookup(t.Context(), "missing")
	if node != nil {
		t.Error("Lookup: node not nil")
	}
	if !errors.Is(err, proto.ENOENT) {
		t.Errorf("Lookup error = %v, want ENOENT", err)
	}
}

func TestInodeAddChildAndLookup(t *testing.T) {
	t.Parallel()

	parent := &Inode{}
	parent.Init(proto.QID{Type: proto.QTDIR, Path: 1}, nil)

	child := &testInodeNode{}
	child.Init(proto.QID{Type: proto.QTFILE, Path: 2}, child)

	parent.AddChild("readme.txt", &child.Inode)

	node, err := parent.Lookup(t.Context(), "readme.txt")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if node.QID().Path != 2 {
		t.Errorf("Lookup QID.Path = %d, want 2", node.QID().Path)
	}
}

func TestInodeChildren(t *testing.T) {
	t.Parallel()

	parent := &Inode{}
	parent.Init(proto.QID{Type: proto.QTDIR, Path: 1}, nil)

	c1 := &Inode{}
	c1.Init(proto.QID{Type: proto.QTFILE, Path: 10}, nil)
	c2 := &Inode{}
	c2.Init(proto.QID{Type: proto.QTFILE, Path: 20}, nil)

	parent.AddChild("a", c1)
	parent.AddChild("b", c2)

	children := parent.Children()
	if len(children) != 2 {
		t.Fatalf("Children count = %d, want 2", len(children))
	}
	if children["a"] != c1 {
		t.Error("Children[a] mismatch")
	}
	if children["b"] != c2 {
		t.Error("Children[b] mismatch")
	}

	// Verify it's a copy by modifying the returned map.
	delete(children, "a")
	if got := parent.Children(); len(got) != 2 {
		t.Error("Children returned live map, not copy")
	}
}

func TestInodeParent(t *testing.T) {
	t.Parallel()

	parent := &Inode{}
	parent.Init(proto.QID{Type: proto.QTDIR, Path: 1}, nil)

	child := &Inode{}
	child.Init(proto.QID{Type: proto.QTFILE, Path: 2}, nil)

	if child.Parent() != nil {
		t.Error("Parent should be nil before AddChild")
	}

	parent.AddChild("file", child)

	if child.Parent() != parent {
		t.Error("Parent should be parent after AddChild")
	}
}

func TestInodeRemoveChild(t *testing.T) {
	t.Parallel()

	parent := &Inode{}
	parent.Init(proto.QID{Type: proto.QTDIR, Path: 1}, nil)

	child := &Inode{}
	child.Init(proto.QID{Type: proto.QTFILE, Path: 2}, nil)

	parent.AddChild("file", child)
	parent.RemoveChild("file")

	_, err := parent.Lookup(t.Context(), "file")
	if !errors.Is(err, proto.ENOENT) {
		t.Errorf("Lookup after RemoveChild: error = %v, want ENOENT", err)
	}
}

func TestComposableReadOnlyFile(t *testing.T) {
	t.Parallel()

	var rof ReadOnlyFile
	rof.Init(proto.QID{Type: proto.QTFILE, Path: 100}, nil)

	// ReadOnlyFile embeds Inode, so it has all defaults.
	if _, ok := any(&rof).(NodeReader); !ok {
		t.Error("ReadOnlyFile does not satisfy NodeReader (via Inode)")
	}

	// Ensure the embedded Inode's Write returns ENOSYS.
	_, err := rof.Write(t.Context(), nil, 0)
	if !errors.Is(err, proto.ENOSYS) {
		t.Errorf("ReadOnlyFile.Write = %v, want ENOSYS", err)
	}
}

func TestComposableReadOnlyDir(t *testing.T) {
	t.Parallel()

	var rod ReadOnlyDir
	rod.Init(proto.QID{Type: proto.QTDIR, Path: 200}, nil)

	// Embedded Inode defaults provide all interfaces.
	if _, ok := any(&rod).(NodeReaddirer); !ok {
		t.Error("ReadOnlyDir does not satisfy NodeReaddirer (via Inode)")
	}

	// Write and Create return ENOSYS via Inode defaults.
	_, err := rod.Write(t.Context(), nil, 0)
	if !errors.Is(err, proto.ENOSYS) {
		t.Errorf("ReadOnlyDir.Write = %v, want ENOSYS", err)
	}
	_, _, _, err = rod.Create(t.Context(), "x", 0, 0, 0)
	if !errors.Is(err, proto.ENOSYS) {
		t.Errorf("ReadOnlyDir.Create = %v, want ENOSYS", err)
	}
}

package memfs

import (
	"context"
	"fmt"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

// NewDir creates a new MemDir as the entry point for fluent tree
// construction. The returned directory's QID is assigned from gen with
// type QTDIR. All child nodes created via builder methods share the
// same generator.
//
// Example:
//
//	root := memfs.NewDir(gen).
//	    AddFile("config.json", configData).
//	    AddStaticFile("version", "1.0.0").
//	    WithDir("data", func(d *memfs.MemDir) {
//	        d.AddFile("cache.db", nil)
//	    })
func NewDir(gen *server.QIDGenerator) *MemDir {
	d := &MemDir{gen: gen}
	d.Init(gen.Next(proto.QTDIR), d)
	return d
}

// AddFile creates a new MemFile child with the given data and default
// mode (0o644). Returns the parent directory for chaining.
func (d *MemDir) AddFile(name string, data []byte) *MemDir {
	return d.AddFileWithMode(name, data, 0o644)
}

// AddFileWithMode creates a new MemFile child with the given data and
// custom mode. Returns the parent directory for chaining.
func (d *MemDir) AddFileWithMode(name string, data []byte, mode uint32) *MemDir {
	child := &MemFile{Data: data, Mode: mode}
	child.Init(d.gen.Next(proto.QTFILE), child)
	d.AddChild(name, child.EmbeddedInode())
	return d
}

// AddStaticFile creates a new StaticFile child with the given content
// and default mode (0o444). Returns the parent directory for chaining.
func (d *MemDir) AddStaticFile(name string, content string) *MemDir {
	child := &StaticFile{Content: content}
	child.Init(d.gen.Next(proto.QTFILE), child)
	d.AddChild(name, child.EmbeddedInode())
	return d
}

// AddDir creates a new MemDir child and registers it in the Inode tree.
// Returns the parent directory for chaining (not the child). Use SubDir
// or WithDir for nested construction.
func (d *MemDir) AddDir(name string) *MemDir {
	child := &MemDir{gen: d.gen}
	child.Init(d.gen.Next(proto.QTDIR), child)
	d.AddChild(name, child.EmbeddedInode())
	return d
}

// SubDir retrieves an existing child directory by name for further
// construction. Panics if the name does not exist or the child is not
// a *MemDir. This is intended for construction-time use only.
func (d *MemDir) SubDir(name string) *MemDir {
	node, err := d.Lookup(context.Background(), name)
	if err != nil {
		panic(fmt.Sprintf("memfs: SubDir(%q): %v", name, err))
	}
	child, ok := node.(*MemDir)
	if !ok {
		panic(fmt.Sprintf("memfs: SubDir(%q): child is %T, not *MemDir", name, node))
	}
	return child
}

// WithDir creates a new child directory named name, calls fn with the
// child for nested construction, and returns the parent directory for
// chaining. Combines AddDir and SubDir into a callback pattern.
func (d *MemDir) WithDir(name string, fn func(*MemDir)) *MemDir {
	child := &MemDir{gen: d.gen}
	child.Init(d.gen.Next(proto.QTDIR), child)
	d.AddChild(name, child.EmbeddedInode())
	fn(child)
	return d
}

// AddSymlink creates a symbolic link child pointing to target. Returns
// the parent directory for chaining.
func (d *MemDir) AddSymlink(name string, target string) *MemDir {
	s := server.SymlinkTo(d.gen, target)
	d.AddChild(name, s.EmbeddedInode())
	return d
}

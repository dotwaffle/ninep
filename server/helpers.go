package server

import (
	"context"

	"github.com/dotwaffle/ninep/proto"
)

// Compile-time assertions for interface compliance.
var (
	_ NodeReadlinker = (*Symlink)(nil)
	_ NodeStatFSer   = (*StaticFS)(nil)
	_ InodeEmbedder  = (*Symlink)(nil)
	_ InodeEmbedder  = (*Device)(nil)
	_ InodeEmbedder  = (*StaticFS)(nil)
)

// Symlink is a node that represents a symbolic link. It implements
// NodeReadlinker to return the link target. Create with SymlinkTo.
type Symlink struct {
	Inode
	Target string
}

// SymlinkTo creates a symbolic link node pointing to target.
// The node's QID is assigned from gen with type QTSYMLINK.
// The returned node implements NodeReadlinker.
func SymlinkTo(gen *QIDGenerator, target string) *Symlink {
	s := &Symlink{Target: target}
	s.Init(gen.Next(proto.QTSYMLINK), s)
	return s
}

// Readlink implements NodeReadlinker.
func (s *Symlink) Readlink(_ context.Context) (string, error) {
	return s.Target, nil
}

// Device is a node that represents a device node (block or character).
// Create with DeviceNode.
type Device struct {
	Inode
	Major uint32
	Minor uint32
}

// DeviceNode creates a device node with the given major and minor numbers.
// The node's QID is assigned from gen with type QTFILE.
func DeviceNode(gen *QIDGenerator, major, minor uint32) *Device {
	d := &Device{Major: major, Minor: minor}
	d.Init(gen.Next(proto.QTFILE), d)
	return d
}

// StaticFS is a node that returns fixed filesystem statistics.
// It implements NodeStatFSer. Embed or compose with other node
// types to add StatFS support.
type StaticFS struct {
	Inode
	Stat proto.FSStat
}

// StaticStatFS creates a node that returns the given filesystem statistics.
// The node's QID is assigned from gen with type QTFILE.
func StaticStatFS(gen *QIDGenerator, stat proto.FSStat) *StaticFS {
	f := &StaticFS{Stat: stat}
	f.Init(gen.Next(proto.QTFILE), f)
	return f
}

// StatFS implements NodeStatFSer.
func (f *StaticFS) StatFS(_ context.Context) (proto.FSStat, error) {
	return f.Stat, nil
}

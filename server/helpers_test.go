package server

import (
	"context"
	"testing"

	"github.com/dotwaffle/ninep/proto"
)

func TestSymlinkTo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target string
	}{
		{name: "absolute path", target: "/tmp/target"},
		{name: "relative path", target: "../other"},
		{name: "empty target", target: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gen := &QIDGenerator{}
			s := SymlinkTo(gen, tt.target)

			// Verify QID type is QTSYMLINK.
			if got := s.QID().Type; got != proto.QTSYMLINK {
				t.Errorf("QID().Type = %v, want %v", got, proto.QTSYMLINK)
			}

			// Verify Readlink returns the target.
			got, err := s.Readlink(context.Background())
			if err != nil {
				t.Fatalf("Readlink() error = %v", err)
			}
			if got != tt.target {
				t.Errorf("Readlink() = %q, want %q", got, tt.target)
			}

			// Verify InodeEmbedder.
			if s.EmbeddedInode() == nil {
				t.Error("EmbeddedInode() returned nil")
			}
		})
	}
}

func TestDeviceNode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		major uint32
		minor uint32
	}{
		{name: "null device", major: 1, minor: 3},
		{name: "zero device", major: 1, minor: 5},
		{name: "loop device", major: 7, minor: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gen := &QIDGenerator{}
			d := DeviceNode(gen, tt.major, tt.minor)

			// Verify QID type is QTFILE.
			if got := d.QID().Type; got != proto.QTFILE {
				t.Errorf("QID().Type = %v, want %v", got, proto.QTFILE)
			}

			// Verify major/minor fields.
			if d.Major != tt.major {
				t.Errorf("Major = %d, want %d", d.Major, tt.major)
			}
			if d.Minor != tt.minor {
				t.Errorf("Minor = %d, want %d", d.Minor, tt.minor)
			}

			// Verify InodeEmbedder.
			if d.EmbeddedInode() == nil {
				t.Error("EmbeddedInode() returned nil")
			}
		})
	}
}

func TestStaticStatFS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		stat proto.FSStat
	}{
		{
			name: "basic stats",
			stat: proto.FSStat{
				Type:    0x6969,
				BSize:   4096,
				Blocks:  1000,
				BFree:   500,
				BAvail:  400,
				Files:   100,
				FFree:   50,
				FSID:    42,
				NameLen: 255,
			},
		},
		{
			name: "zero stats",
			stat: proto.FSStat{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gen := &QIDGenerator{}
			f := StaticStatFS(gen, tt.stat)

			// Verify QID type is QTFILE.
			if got := f.QID().Type; got != proto.QTFILE {
				t.Errorf("QID().Type = %v, want %v", got, proto.QTFILE)
			}

			// Verify StatFS returns the provided stat.
			got, err := f.StatFS(context.Background())
			if err != nil {
				t.Fatalf("StatFS() error = %v", err)
			}
			if got != tt.stat {
				t.Errorf("StatFS() = %+v, want %+v", got, tt.stat)
			}

			// Verify InodeEmbedder.
			if f.EmbeddedInode() == nil {
				t.Error("EmbeddedInode() returned nil")
			}
		})
	}
}

func TestHelperQIDUniqueness(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}
	s := SymlinkTo(gen, "/target")
	d := DeviceNode(gen, 1, 3)
	f := StaticStatFS(gen, proto.FSStat{})

	paths := map[uint64]string{
		s.QID().Path: "Symlink",
		d.QID().Path: "Device",
		f.QID().Path: "StaticFS",
	}
	if len(paths) != 3 {
		t.Errorf("expected 3 unique QID paths, got %d: %v", len(paths), paths)
	}
}

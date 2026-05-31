package server

import (
	"testing"

	"github.com/dotwaffle/ninep/proto"
)

// TestWithLogger_NilIsIgnored asserts a nil logger does not panic at
// construction and leaves the default logger in place.
func TestWithLogger_NilIsIgnored(t *testing.T) {
	t.Parallel()
	root := newDirNode(proto.QID{Type: proto.QTDIR, Path: 1})

	var srv *Server
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("New with WithLogger(nil) panicked: %v", r)
			}
		}()
		srv = New(root, WithLogger(nil))
	}()
	if srv.logger == nil {
		t.Fatal("WithLogger(nil) left the server with no logger")
	}
}

// TestWithMaxMsize_ClampsToMinimum asserts an msize below the protocol minimum
// is clamped up so the server can still negotiate connections.
func TestWithMaxMsize_ClampsToMinimum(t *testing.T) {
	t.Parallel()
	root := newDirNode(proto.QID{Type: proto.QTDIR, Path: 1})

	tests := []struct {
		in, want uint32
	}{
		{0, minMsize},
		{100, minMsize},
		{minMsize, minMsize},
		{65536, 65536},
	}
	for _, tt := range tests {
		if got := New(root, WithMaxMsize(tt.in)).maxMsize; got != tt.want {
			t.Errorf("WithMaxMsize(%d): maxMsize = %d, want %d", tt.in, got, tt.want)
		}
	}
}

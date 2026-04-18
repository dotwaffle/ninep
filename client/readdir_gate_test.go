package client

// Internal-package dialect-gate test for File.ReadDir. Uses newGateConn
// from dialect_gate_test.go to build a minimal *Conn with dialect set
// to protocolU without spawning a real server, because the ReadDir
// dialect check fires at method entry before any wire activity.

import (
	"errors"
	"testing"

	"github.com/dotwaffle/ninep/proto"
)

// TestFileReadDir_NotSupportedOnU: File.ReadDir on a .u Conn returns
// (nil, wrapped ErrNotSupported). Q4 resolution.
func TestFileReadDir_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	c := newGateConn(t, protocolU)

	f := &File{
		conn:   c,
		fid:    proto.Fid(1),
		qid:    proto.QID{Type: proto.QTDIR, Path: 1},
		iounit: 0,
	}

	entries, err := f.ReadDir(-1)
	if !errors.Is(err, ErrNotSupported) {
		t.Fatalf("ReadDir err = %v, want ErrNotSupported", err)
	}
	if entries != nil {
		t.Errorf("ReadDir entries = %v, want nil", entries)
	}
}

package server

import (
	"testing"

	"github.com/dotwaffle/ninep/proto"
)

func TestNew_RejectsNilLogger(t *testing.T) {
	t.Parallel()
	root := newDirNode(proto.QID{Type: proto.QTDIR, Path: 1})
	if _, err := New(root, WithLogger(nil)); err == nil {
		t.Fatal("New with WithLogger(nil) succeeded, want error")
	}
}

func TestNew_RejectsInvalidMsize(t *testing.T) {
	t.Parallel()
	root := newDirNode(proto.QID{Type: proto.QTDIR, Path: 1})

	for _, msize := range []uint32{
		0,
		minMsize - 1,
		proto.MaxMessageSize + 1,
		^uint32(0),
	} {
		if _, err := New(root, WithMaxMsize(msize)); err == nil {
			t.Errorf("New with msize %d succeeded, want error", msize)
		}
	}

	for _, msize := range []uint32{minMsize, 65536, proto.MaxMessageSize} {
		srv, err := New(root, WithMaxMsize(msize))
		if err != nil {
			t.Errorf("New with msize %d: %v", msize, err)
			continue
		}
		if srv.maxMsize != msize {
			t.Errorf("New with msize %d stored %d", msize, srv.maxMsize)
		}
	}
}

func TestNew_DefaultResourceBounds(t *testing.T) {
	t.Parallel()
	root := newDirNode(proto.QID{Type: proto.QTDIR, Path: 1})
	srv, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if srv.maxConnections != defaultMaxConnections || srv.maxFids != defaultMaxFids || srv.idleTimeout != defaultIdleTimeout {
		t.Fatalf("default bounds = connections:%d fids:%d idle:%s", srv.maxConnections, srv.maxFids, srv.idleTimeout)
	}

	trusted, err := New(root, WithTrustedNetwork())
	if err != nil {
		t.Fatal(err)
	}
	if trusted.maxConnections != 0 || trusted.maxFids != 0 || trusted.idleTimeout != 0 {
		t.Fatalf("trusted bounds = connections:%d fids:%d idle:%s", trusted.maxConnections, trusted.maxFids, trusted.idleTimeout)
	}
}

func TestNew_RejectsNilDependencies(t *testing.T) {
	t.Parallel()
	root := newDirNode(proto.QID{Type: proto.QTDIR, Path: 1})
	for name, opt := range map[string]Option{
		"tracer":     WithTracer(nil),
		"meter":      WithMeter(nil),
		"middleware": WithMiddleware(nil),
		"attacher":   WithAttacher(nil),
		"aname root": WithAnames(map[string]Node{"data": nil}),
	} {
		if _, err := New(root, opt); err == nil {
			t.Errorf("New accepted nil %s", name)
		}
	}
}

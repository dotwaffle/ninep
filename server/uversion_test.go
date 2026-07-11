package server

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
	"github.com/dotwaffle/ninep/proto/p9u"
)

func sendMessageU(t *testing.T, w io.Writer, tag proto.Tag, msg proto.Message) {
	t.Helper()
	if err := p9u.Encode(w, tag, msg); err != nil {
		t.Fatalf("encode %s: %v", msg.Type(), err)
	}
}

func readResponseU(t *testing.T, r io.Reader) (proto.Tag, proto.Message) {
	t.Helper()
	tag, msg, err := p9u.Decode(r)
	if err != nil {
		t.Fatalf("decode .u response: %v", err)
	}
	return tag, msg
}

func newUConnPair(t *testing.T, root Node) (*connPair, *proto.Rattach) {
	t.Helper()

	srv := MustNew(root, WithMaxMsize(65536), WithLogger(discardLogger()))
	client, serverConn := net.Pipe()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.ServeConn(ctx, serverConn)
	}()

	t.Cleanup(func() {
		_ = client.Close()
		cancel()
		<-done
	})

	sendTversion(t, client, 65536, "9P2000.u")
	rv := readRversion(t, client)
	if rv.Version != "9P2000.u" {
		t.Fatalf("version = %q, want 9P2000.u", rv.Version)
	}

	sendMessageU(t, client, 1, &proto.Tattach{
		Fid:   0,
		Afid:  proto.NoFid,
		Uname: "test",
		Aname: "",
	})
	tag, msg := readResponseU(t, client)
	if tag != 1 {
		t.Fatalf("attach tag = %d, want 1", tag)
	}
	rattach, ok := msg.(*proto.Rattach)
	if !ok {
		t.Fatalf("expected Rattach, got %T", msg)
	}

	return &connPair{client: client, done: done, cancel: cancel}, rattach
}

func TestServerUVersion_OpenCreateStat(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}
	root := &bridgeDir{gen: gen}
	root.Init(gen.Next(proto.QTDIR), root)
	child := &bridgeFile{content: []byte("hello"), mode: 0o644}
	child.Init(gen.Next(proto.QTFILE), child)
	root.AddChild("file", child.EmbeddedInode())

	cp, _ := newUConnPair(t, root)

	sendMessageU(t, cp.client, 2, &proto.Twalk{Fid: 0, NewFid: 1, Names: []string{"file"}})
	tag, msg := readResponseU(t, cp.client)
	if tag != 2 {
		t.Fatalf("walk tag = %d, want 2", tag)
	}
	rwalk, ok := msg.(*proto.Rwalk)
	if !ok {
		t.Fatalf("expected Rwalk, got %T", msg)
	}
	if len(rwalk.QIDs) != 1 || rwalk.QIDs[0] != child.QID() {
		t.Fatalf("walk qids = %+v, want child qid %+v", rwalk.QIDs, child.QID())
	}

	sendMessageU(t, cp.client, 3, &p9u.Tstat{Fid: 1})
	tag, msg = readResponseU(t, cp.client)
	if tag != 3 {
		t.Fatalf("stat tag = %d, want 3", tag)
	}
	rstat, ok := msg.(*p9u.Rstat)
	if !ok {
		t.Fatalf("expected Rstat, got %T", msg)
	}
	if rstat.Stat.QID != child.QID() {
		t.Fatalf("stat qid = %+v, want %+v", rstat.Stat.QID, child.QID())
	}
	if rstat.Stat.Length != uint64(len(child.content)) {
		t.Fatalf("stat length = %d, want %d", rstat.Stat.Length, len(child.content))
	}

	sendMessageU(t, cp.client, 4, &p9u.Topen{Fid: 1, Mode: 0})
	tag, msg = readResponseU(t, cp.client)
	if tag != 4 {
		t.Fatalf("open tag = %d, want 4", tag)
	}
	ropen, ok := msg.(*p9u.Ropen)
	if !ok {
		t.Fatalf("expected Ropen, got %T", msg)
	}
	if ropen.QID != child.QID() {
		t.Fatalf("open qid = %+v, want %+v", ropen.QID, child.QID())
	}

	sendMessageU(t, cp.client, 5, &proto.Twalk{Fid: 0, NewFid: 2})
	tag, msg = readResponseU(t, cp.client)
	if tag != 5 {
		t.Fatalf("zero walk tag = %d, want 5", tag)
	}
	if _, ok := msg.(*proto.Rwalk); !ok {
		t.Fatalf("expected Rwalk, got %T", msg)
	}

	sendMessageU(t, cp.client, 6, &p9u.Tcreate{
		Fid:  2,
		Name: "new",
		Perm: 0o644,
		Mode: 1,
	})
	tag, msg = readResponseU(t, cp.client)
	if tag != 6 {
		t.Fatalf("create tag = %d, want 6", tag)
	}
	rcreate, ok := msg.(*p9u.Rcreate)
	if !ok {
		t.Fatalf("expected Rcreate, got %T", msg)
	}
	if rcreate.QID.Type != proto.QTFILE {
		t.Fatalf("create qid type = %d, want QTFILE", rcreate.QID.Type)
	}
}

func TestServerUVersion_RejectsLSpecificMessages(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}
	root := &bridgeDir{gen: gen}
	root.Init(gen.Next(proto.QTDIR), root)
	cp, _ := newUConnPair(t, root)

	sendMessage(t, cp.client, 2, &p9l.Tlopen{Fid: 0, Flags: 0})
	tag, msg := readResponseU(t, cp.client)
	if tag != 2 {
		t.Fatalf("reject tag = %d, want 2", tag)
	}
	rerr, ok := msg.(*p9u.Rerror)
	if !ok {
		t.Fatalf("expected Rerror for .L message on .u connection, got %T", msg)
	}
	if rerr.Errno != proto.ENOSYS {
		t.Fatalf("errno = %v, want ENOSYS", rerr.Errno)
	}
}

package server

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// testDir implements Node and NodeLookuper for walk tests.
type testDir struct {
	qid      proto.QID
	children map[string]Node
}

func (d *testDir) QID() proto.QID { return d.qid }

func (d *testDir) Lookup(_ context.Context, name string) (Node, error) {
	child, ok := d.children[name]
	if !ok {
		return nil, proto.ENOENT
	}
	return child, nil
}

// testFile implements Node but NOT NodeLookuper (not a directory).
type testFile struct {
	qid proto.QID
}

func (f *testFile) QID() proto.QID { return f.qid }

// Compile-time checks.
var (
	_ Node          = (*testDir)(nil)
	_ NodeLookuper  = (*testDir)(nil)
	_ Node          = (*testFile)(nil)
)

// testTree builds a filesystem tree for walk tests:
//
//	root (dir, path=1) -> "sub" (dir, path=2) -> "file.txt" (file, path=3)
//	                   -> "other" (file, path=4)
func testTree() *testDir {
	file := &testFile{qid: proto.QID{Type: proto.QTFILE, Version: 0, Path: 3}}
	sub := &testDir{
		qid:      proto.QID{Type: proto.QTDIR, Version: 0, Path: 2},
		children: map[string]Node{"file.txt": file},
	}
	other := &testFile{qid: proto.QID{Type: proto.QTFILE, Version: 0, Path: 4}}
	root := &testDir{
		qid: proto.QID{Type: proto.QTDIR, Version: 0, Path: 1},
		children: map[string]Node{
			"sub":   sub,
			"other": other,
		},
	}
	return root
}

// connPair creates a server serving the given root and returns the client-side
// connection, a done channel, and a cancel function. The caller must call
// cancel() after the test.
type connPair struct {
	client net.Conn
	done   chan struct{}
	cancel context.CancelFunc
}

func newConnPair(t *testing.T, root Node, opts ...Option) *connPair {
	t.Helper()

	opts = append([]Option{WithMaxMsize(65536), WithLogger(discardLogger())}, opts...)
	srv := New(root, opts...)

	client, server := net.Pipe()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(func() {
		cancel()
		client.Close()
		server.Close()
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.ServeConn(ctx, server)
	}()

	// Negotiate version.
	sendTversion(t, client, 65536, "9P2000.L")
	rv := readRversion(t, client)
	if rv.Version != "9P2000.L" {
		t.Fatalf("version negotiation failed: got %q", rv.Version)
	}

	return &connPair{client: client, done: done, cancel: cancel}
}

func (cp *connPair) close(t *testing.T) {
	t.Helper()
	cp.client.Close()
	<-cp.done
	cp.cancel()
}

// attach sends a Tattach and returns the Rattach. Fatal on error response.
func (cp *connPair) attach(t *testing.T, tag proto.Tag, fid proto.Fid, uname, aname string) *proto.Rattach {
	t.Helper()
	sendMessage(t, cp.client, tag, &proto.Tattach{
		Fid:   fid,
		Afid:  proto.NoFid,
		Uname: uname,
		Aname: aname,
	})
	_, msg := readResponse(t, cp.client)
	ra, ok := msg.(*proto.Rattach)
	if !ok {
		t.Fatalf("expected Rattach, got %T: %+v", msg, msg)
	}
	return ra
}

// walk sends a Twalk and returns the raw response message.
func (cp *connPair) walk(t *testing.T, tag proto.Tag, fid, newfid proto.Fid, names ...string) proto.Message {
	t.Helper()
	sendMessage(t, cp.client, tag, &proto.Twalk{
		Fid:    fid,
		NewFid: newfid,
		Names:  names,
	})
	_, msg := readResponse(t, cp.client)
	return msg
}

// clunk sends a Tclunk and returns the raw response message.
func (cp *connPair) clunk(t *testing.T, tag proto.Tag, fid proto.Fid) proto.Message {
	t.Helper()
	sendMessage(t, cp.client, tag, &proto.Tclunk{Fid: fid})
	_, msg := readResponse(t, cp.client)
	return msg
}

// isError checks if a message is an Rlerror with the expected errno.
func isError(t *testing.T, msg proto.Message, want proto.Errno) {
	t.Helper()
	rlerr, ok := msg.(*p9l.Rlerror)
	if !ok {
		t.Fatalf("expected Rlerror, got %T: %+v", msg, msg)
	}
	if rlerr.Ecode != want {
		t.Errorf("errno = %d (%s), want %d (%s)", rlerr.Ecode, rlerr.Ecode, want, want)
	}
}

func TestAttach_DefaultRoot(t *testing.T) {
	t.Parallel()
	root := testTree()
	cp := newConnPair(t, root)
	defer cp.close(t)

	ra := cp.attach(t, 1, 0, "user", "")
	if ra.QID != root.QID() {
		t.Errorf("QID = %+v, want %+v", ra.QID, root.QID())
	}
}

func TestAttach_WithAnames(t *testing.T) {
	t.Parallel()
	root := testTree()
	altRoot := &testDir{
		qid:      proto.QID{Type: proto.QTDIR, Version: 0, Path: 99},
		children: map[string]Node{},
	}
	anames := map[string]Node{"alt": altRoot}
	cp := newConnPair(t, root, WithAnames(anames))
	defer cp.close(t)

	ra := cp.attach(t, 1, 0, "user", "alt")
	if ra.QID != altRoot.QID() {
		t.Errorf("QID = %+v, want %+v", ra.QID, altRoot.QID())
	}
}

type testAttacher struct {
	node Node
	err  error
}

func (a *testAttacher) Attach(_ context.Context, _, _ string) (Node, error) {
	return a.node, a.err
}

func TestAttach_WithAttacher(t *testing.T) {
	t.Parallel()
	root := testTree()
	customNode := &testFile{qid: proto.QID{Type: proto.QTFILE, Path: 77}}
	att := &testAttacher{node: customNode}
	cp := newConnPair(t, root, WithAttacher(att))
	defer cp.close(t)

	ra := cp.attach(t, 1, 0, "user", "anything")
	if ra.QID != customNode.QID() {
		t.Errorf("QID = %+v, want %+v", ra.QID, customNode.QID())
	}
}

func TestAttach_FidInUse(t *testing.T) {
	t.Parallel()
	root := testTree()
	cp := newConnPair(t, root)
	defer cp.close(t)

	// First attach succeeds.
	cp.attach(t, 1, 0, "user", "")

	// Second attach with same fid fails.
	sendMessage(t, cp.client, 2, &proto.Tattach{
		Fid:   0,
		Afid:  proto.NoFid,
		Uname: "user",
		Aname: "",
	})
	_, msg := readResponse(t, cp.client)
	isError(t, msg, proto.EBADF)
}

func TestWalk_CloneNewFid(t *testing.T) {
	t.Parallel()
	root := testTree()
	cp := newConnPair(t, root)
	defer cp.close(t)

	cp.attach(t, 1, 0, "user", "")

	// nwname=0, fid!=newfid: clone fid.
	msg := cp.walk(t, 2, 0, 1)
	rw, ok := msg.(*proto.Rwalk)
	if !ok {
		t.Fatalf("expected Rwalk, got %T: %+v", msg, msg)
	}
	if len(rw.QIDs) != 0 {
		t.Errorf("clone walk QIDs = %d, want 0", len(rw.QIDs))
	}

	// Verify newfid works by clunking it.
	clunkMsg := cp.clunk(t, 3, 1)
	if _, ok := clunkMsg.(*proto.Rclunk); !ok {
		t.Fatalf("expected Rclunk for cloned fid, got %T", clunkMsg)
	}
}

func TestWalk_CloneSameFid(t *testing.T) {
	t.Parallel()
	root := testTree()
	cp := newConnPair(t, root)
	defer cp.close(t)

	cp.attach(t, 1, 0, "user", "")

	// nwname=0, fid==newfid: no-op.
	msg := cp.walk(t, 2, 0, 0)
	rw, ok := msg.(*proto.Rwalk)
	if !ok {
		t.Fatalf("expected Rwalk, got %T: %+v", msg, msg)
	}
	if len(rw.QIDs) != 0 {
		t.Errorf("no-op walk QIDs = %d, want 0", len(rw.QIDs))
	}
}

func TestWalk_ThreeElements(t *testing.T) {
	t.Parallel()

	// Build deeper tree: root -> "a" -> "b" -> "c" (file).
	cFile := &testFile{qid: proto.QID{Type: proto.QTFILE, Path: 30}}
	bDir := &testDir{
		qid:      proto.QID{Type: proto.QTDIR, Path: 20},
		children: map[string]Node{"c": cFile},
	}
	aDir := &testDir{
		qid:      proto.QID{Type: proto.QTDIR, Path: 10},
		children: map[string]Node{"b": bDir},
	}
	root := &testDir{
		qid:      proto.QID{Type: proto.QTDIR, Path: 1},
		children: map[string]Node{"a": aDir},
	}

	cp := newConnPair(t, root)
	defer cp.close(t)
	cp.attach(t, 1, 0, "user", "")

	msg := cp.walk(t, 2, 0, 1, "a", "b", "c")
	rw, ok := msg.(*proto.Rwalk)
	if !ok {
		t.Fatalf("expected Rwalk, got %T: %+v", msg, msg)
	}
	if len(rw.QIDs) != 3 {
		t.Fatalf("walk QIDs count = %d, want 3", len(rw.QIDs))
	}
	wantQIDs := []proto.QID{aDir.QID(), bDir.QID(), cFile.QID()}
	for i, want := range wantQIDs {
		if rw.QIDs[i] != want {
			t.Errorf("QID[%d] = %+v, want %+v", i, rw.QIDs[i], want)
		}
	}
}

func TestWalk_FirstElementFails(t *testing.T) {
	t.Parallel()
	root := testTree()
	cp := newConnPair(t, root)
	defer cp.close(t)
	cp.attach(t, 1, 0, "user", "")

	// Walk to nonexistent name -- first element fails -> Rerror (not Rwalk with 0 QIDs).
	msg := cp.walk(t, 2, 0, 1, "nonexistent")
	isError(t, msg, proto.ENOENT)
}

func TestWalk_SecondElementFails(t *testing.T) {
	t.Parallel()
	root := testTree()
	cp := newConnPair(t, root)
	defer cp.close(t)
	cp.attach(t, 1, 0, "user", "")

	// Walk "sub", "nonexistent" -- second element fails -> partial Rwalk with 1 QID.
	msg := cp.walk(t, 2, 0, 1, "sub", "nonexistent")
	rw, ok := msg.(*proto.Rwalk)
	if !ok {
		t.Fatalf("expected Rwalk for partial walk, got %T: %+v", msg, msg)
	}
	if len(rw.QIDs) != 1 {
		t.Fatalf("partial walk QIDs = %d, want 1", len(rw.QIDs))
	}
	subQID := proto.QID{Type: proto.QTDIR, Version: 0, Path: 2}
	if rw.QIDs[0] != subQID {
		t.Errorf("QID[0] = %+v, want %+v", rw.QIDs[0], subQID)
	}

	// newfid should NOT be assigned on partial walk.
	// Verify by trying to clunk newfid=1 -- should fail.
	clunkMsg := cp.clunk(t, 3, 1)
	isError(t, clunkMsg, proto.EBADF)
}

func TestWalk_SameFidAllSucceed(t *testing.T) {
	t.Parallel()
	root := testTree()
	cp := newConnPair(t, root)
	defer cp.close(t)
	cp.attach(t, 1, 0, "user", "")

	// Walk fid==newfid, all succeed -- updates fid in place.
	msg := cp.walk(t, 2, 0, 0, "sub")
	rw, ok := msg.(*proto.Rwalk)
	if !ok {
		t.Fatalf("expected Rwalk, got %T: %+v", msg, msg)
	}
	if len(rw.QIDs) != 1 {
		t.Fatalf("walk QIDs = %d, want 1", len(rw.QIDs))
	}
	subQID := proto.QID{Type: proto.QTDIR, Version: 0, Path: 2}
	if rw.QIDs[0] != subQID {
		t.Errorf("QID[0] = %+v, want %+v", rw.QIDs[0], subQID)
	}

	// Now fid 0 should point to "sub". Walk from it to "file.txt".
	msg = cp.walk(t, 3, 0, 0, "file.txt")
	rw, ok = msg.(*proto.Rwalk)
	if !ok {
		t.Fatalf("expected Rwalk, got %T: %+v", msg, msg)
	}
	fileQID := proto.QID{Type: proto.QTFILE, Version: 0, Path: 3}
	if len(rw.QIDs) != 1 || rw.QIDs[0] != fileQID {
		t.Errorf("walk to file.txt: QIDs = %+v, want [%+v]", rw.QIDs, fileQID)
	}
}

func TestWalk_SameFidPartialFail(t *testing.T) {
	t.Parallel()
	root := testTree()
	cp := newConnPair(t, root)
	defer cp.close(t)
	cp.attach(t, 1, 0, "user", "")

	// Walk fid==newfid, partial failure -- fid should remain unchanged.
	msg := cp.walk(t, 2, 0, 0, "sub", "nonexistent")
	rw, ok := msg.(*proto.Rwalk)
	if !ok {
		t.Fatalf("expected Rwalk, got %T: %+v", msg, msg)
	}
	if len(rw.QIDs) != 1 {
		t.Fatalf("partial walk QIDs = %d, want 1", len(rw.QIDs))
	}

	// Fid 0 should still point to root. Walk "sub" from it to verify.
	msg = cp.walk(t, 3, 0, 1, "sub")
	rw, ok = msg.(*proto.Rwalk)
	if !ok {
		t.Fatalf("expected Rwalk, got %T: %+v", msg, msg)
	}
	subQID := proto.QID{Type: proto.QTDIR, Version: 0, Path: 2}
	if len(rw.QIDs) != 1 || rw.QIDs[0] != subQID {
		t.Errorf("fid should still be root: walk 'sub' gave QIDs = %+v", rw.QIDs)
	}
}

func TestWalk_SourceFidNotFound(t *testing.T) {
	t.Parallel()
	root := testTree()
	cp := newConnPair(t, root)
	defer cp.close(t)

	// Don't attach -- fid 99 doesn't exist.
	msg := cp.walk(t, 1, 99, 100, "anything")
	isError(t, msg, proto.EBADF)
}

func TestWalk_NotDirectory(t *testing.T) {
	t.Parallel()
	root := testTree()
	cp := newConnPair(t, root)
	defer cp.close(t)
	cp.attach(t, 1, 0, "user", "")

	// Walk to "other" (a file), then try to walk into it.
	msg := cp.walk(t, 2, 0, 1, "other")
	rw, ok := msg.(*proto.Rwalk)
	if !ok {
		t.Fatalf("expected Rwalk, got %T: %+v", msg, msg)
	}
	if len(rw.QIDs) != 1 {
		t.Fatalf("walk QIDs = %d, want 1", len(rw.QIDs))
	}

	// Now fid 1 is a file (testFile, no NodeLookuper). Walk from it.
	msg = cp.walk(t, 3, 1, 2, "child")
	isError(t, msg, proto.ENOTDIR)
}

func TestClunk_RemovesFid(t *testing.T) {
	t.Parallel()
	root := testTree()
	cp := newConnPair(t, root)
	defer cp.close(t)
	cp.attach(t, 1, 0, "user", "")

	// Clunk fid 0.
	msg := cp.clunk(t, 2, 0)
	if _, ok := msg.(*proto.Rclunk); !ok {
		t.Fatalf("expected Rclunk, got %T", msg)
	}

	// Verify fid 0 is gone by trying to walk from it.
	msg = cp.walk(t, 3, 0, 1, "sub")
	isError(t, msg, proto.EBADF)
}

func TestClunk_NonexistentFid(t *testing.T) {
	t.Parallel()
	root := testTree()
	cp := newConnPair(t, root)
	defer cp.close(t)

	msg := cp.clunk(t, 1, 99)
	isError(t, msg, proto.EBADF)
}

func TestDispatch_UnknownMessageType(t *testing.T) {
	t.Parallel()
	root := testTree()
	cp := newConnPair(t, root)
	defer cp.close(t)

	// Send a Tread on a non-existent fid. Tread is now a handled message type
	// (dispatched to handleRead), which checks fid existence first, returning
	// EBADF for an unknown fid.
	sendMessage(t, cp.client, 1, &proto.Tread{Fid: 99, Offset: 0, Count: 1024})
	_, msg := readResponse(t, cp.client)
	isError(t, msg, proto.EBADF)
}

func TestAttach_AuthFidRejected(t *testing.T) {
	t.Parallel()
	root := testTree()
	cp := newConnPair(t, root)
	defer cp.close(t)

	// Tattach with Afid != NoFid should be rejected (no auth support).
	sendMessage(t, cp.client, 1, &proto.Tattach{
		Fid:   0,
		Afid:  42, // Not NoFid.
		Uname: "user",
		Aname: "",
	})
	_, msg := readResponse(t, cp.client)
	isError(t, msg, proto.ENOSYS)
}

func TestAttach_TauthRejected(t *testing.T) {
	t.Parallel()
	root := testTree()
	cp := newConnPair(t, root)
	defer cp.close(t)

	// Tauth should return ENOSYS.
	sendMessage(t, cp.client, 1, &proto.Tauth{
		Afid:  0,
		Uname: "user",
		Aname: "",
	})
	_, msg := readResponse(t, cp.client)
	isError(t, msg, proto.ENOSYS)
}

// Suppress unused import warning for io.
var _ = io.Discard

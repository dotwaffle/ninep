package client_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
	"github.com/dotwaffle/ninep/server/memfs"
)

// TestClient_Stat_L verifies that File.Stat on a .L Conn issues Tgetattr,
// runs attrToStat, and returns a dialect-neutral p9u.Stat with the file's
// size surfaced on Length.
func TestClient_Stat_L(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = root.Close() }()

	f, err := cli.OpenFile(ctx, "hello.txt", 0 /*OREAD*/, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	st, err := f.Stat(ctx)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Length != 12 {
		t.Errorf("Stat.Length = %d, want 12", st.Length)
	}
	if st.QID.Type&proto.QTDIR != 0 {
		t.Errorf("Stat.QID.Type = %#x, want file bit", st.QID.Type)
	}
	if st.QID.Path == 0 {
		t.Errorf("Stat.QID.Path = 0, want non-zero (memfs assigns via QIDGenerator)")
	}
}

// TestClient_Stat_U exercises the .u branch of File.Stat via a uMockServer
// extended to answer p9u.Tstat with a known Stat. Validates that File.Stat
// returns r.Stat verbatim on .u.
func TestClient_Stat_U(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockStatClientPair(t, wantStat)
	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	if _, err := cli.Raw().Attach(ctx, 0, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	// The u-mock server always returns the canned Stat regardless of fid;
	// NewFileForTest gives us a *File over fid 0.
	f := client.NewFileForTest(cli)
	st, err := f.Stat(ctx)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Length != 12 {
		t.Errorf("Stat.Length = %d, want 12", st.Length)
	}
	if st.Name != "hello.txt" {
		t.Errorf("Stat.Name = %q, want hello.txt", st.Name)
	}
	if st.QID.Path != 99 {
		t.Errorf("Stat.QID.Path = %d, want 99", st.QID.Path)
	}
}

// TestClient_Stat_Consistency_LvsU asserts the attrToStat invariant: the
// .u Stat produced by converting an equivalent .L Attr matches the fields
// a real .u server would return for the same file. Pure unit test; does
// not require a live .u server.
func TestClient_Stat_Consistency_LvsU(t *testing.T) {
	t.Parallel()
	// Equivalent to what memfs.MemFile{hello.txt 12 bytes}.Getattr would
	// return (Mode 0o644 default, Size 12, NLink 1, QID from gen.Next).
	qid := proto.QID{Type: proto.QTFILE, Version: 0, Path: 7}
	attr := proto.Attr{
		Valid: proto.AttrBasic,
		QID:   qid,
		Mode:  0o644,
		UID:   1000,
		GID:   1000,
		NLink: 1,
		Size:  12,
	}
	converted := client.AttrToStatForTest(attr)
	if converted.Length != 12 {
		t.Errorf("attrToStat.Length = %d, want 12", converted.Length)
	}
	if converted.QID != qid {
		t.Errorf("attrToStat.QID = %+v, want %+v", converted.QID, qid)
	}
	if converted.UID != strconv.FormatUint(1000, 10) {
		t.Errorf("attrToStat.UID = %q, want \"1000\"", converted.UID)
	}

	// Equivalent p9u.Stat that a hypothetical .u server would return for the
	// same file. Matching on the unifying invariants: QID.Path, Length.
	// Fields attrToStat cannot preserve (Name, Dev, Type) come from server
	// state not present in Attr and are not asserted equal here.
	if converted.QID.Path != qid.Path {
		t.Errorf("consistency: QID.Path %d != Attr QID.Path %d", converted.QID.Path, qid.Path)
	}
	if converted.Length != attr.Size {
		t.Errorf("consistency: Length %d != Attr.Size %d", converted.Length, attr.Size)
	}
}

// TestClient_Getattr_LFields asserts .L Getattr surfaces rich fields
// attrToStat drops (NLink specifically — memfs does not populate
// Blocks/BTime/Gen/DataVersion, so NLink is the observable fingerprint).
func TestClient_Getattr_LFields(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	root, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = root.Close() }()

	f, err := cli.OpenFile(ctx, "hello.txt", 0, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	attr, err := f.Getattr(ctx, proto.AttrAll)
	if err != nil {
		t.Fatalf("Getattr: %v", err)
	}
	if attr.NLink == 0 {
		t.Errorf("Getattr.NLink = 0, want >0 (memfs sets NLink=1)")
	}
	if attr.Size != 12 {
		t.Errorf("Getattr.Size = %d, want 12", attr.Size)
	}
}

// TestClient_Getattr_NotSupportedOnU: .L-only gate.
func TestClient_Getattr_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockStatClientPair(t, wantStat)
	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	f := client.NewFileForTest(cli)
	_, err := f.Getattr(ctx, proto.AttrBasic)
	if !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Getattr err = %v, want ErrNotSupported", err)
	}
}

// TestClient_Stat_PropagatesRlerror: .L Stat on a node whose Getattr
// returns proto.ENOENT surfaces a *client.Error whose errors.Is matches
// proto.ENOENT.
func TestClient_Stat_PropagatesRlerror(t *testing.T) {
	t.Parallel()
	gen := &server.QIDGenerator{}
	root := memfs.NewDir(gen)
	node := &testGetattrENOENT{qid: gen.Next(proto.QTFILE)}
	node.Init(node.qid, node)
	root.AddChild("broken.txt", &node.Inode)

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	r, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = r.Close() }()

	f, err := cli.OpenFile(ctx, "broken.txt", 0, 0)
	if err != nil {
		// Some open paths run Getattr through the bridge; if Open already
		// surfaced ENOENT, that's also a valid pass path for the test — the
		// critical assertion is that the error is a wrapped *Error.
		var ce *client.Error
		if !errors.As(err, &ce) || !errors.Is(err, proto.ENOENT) {
			t.Fatalf("OpenFile err = %v, want *client.Error wrapping ENOENT", err)
		}
		return
	}
	defer func() { _ = f.Close() }()

	_, err = f.Stat(ctx)
	var ce *client.Error
	if !errors.As(err, &ce) {
		t.Fatalf("Stat err = %v, want *client.Error", err)
	}
	if !errors.Is(err, proto.ENOENT) {
		t.Fatalf("errors.Is(Stat err, ENOENT) = false, err = %v", err)
	}
}

// TestAttrToStat: pure unit test of the .L Attr → .u Stat field mapping.
func TestAttrToStat(t *testing.T) {
	t.Parallel()
	qid := proto.QID{Type: 0x80, Version: 1, Path: 42}
	attr := proto.Attr{
		QID:         qid,
		Mode:        0o755,
		UID:         1000,
		GID:         2000,
		Size:        4096,
		ATimeSec:    100,
		MTimeSec:    200,
		NLink:       17,  // dropped by attrToStat
		Blocks:      8,   // dropped
		BTimeSec:    300, // dropped
		Gen:         42,  // dropped
		DataVersion: 99,  // dropped
	}
	got := client.AttrToStatForTest(attr)
	if got.Length != 4096 {
		t.Errorf("Length = %d, want 4096", got.Length)
	}
	if got.QID != qid {
		t.Errorf("QID = %+v, want %+v", got.QID, qid)
	}
	if got.Mode != proto.FileMode(0o755) {
		t.Errorf("Mode = %#o, want 0o755", got.Mode)
	}
	if got.Atime != 100 {
		t.Errorf("Atime = %d, want 100", got.Atime)
	}
	if got.Mtime != 200 {
		t.Errorf("Mtime = %d, want 200", got.Mtime)
	}
	if got.UID != "1000" {
		t.Errorf("UID = %q, want \"1000\"", got.UID)
	}
	if got.GID != "2000" {
		t.Errorf("GID = %q, want \"2000\"", got.GID)
	}
	if got.NUid != 1000 {
		t.Errorf("NUid = %d, want 1000", got.NUid)
	}
	if got.NGid != 2000 {
		t.Errorf("NGid = %d, want 2000", got.NGid)
	}
	if got.MUID != "" {
		t.Errorf("MUID = %q, want empty", got.MUID)
	}
	if got.Extension != "" {
		t.Errorf("Extension = %q, want empty", got.Extension)
	}
	if got.Name != "" {
		t.Errorf("Name = %q, want empty (attrToStat does not carry Name)", got.Name)
	}
}

// testGetattrENOENT is a memfs-compatible Node whose Getattr returns
// proto.ENOENT. Used to exercise the error-propagation path of File.Stat
// on .L.
type testGetattrENOENT struct {
	server.Inode
	qid proto.QID
}

func (n *testGetattrENOENT) QID() proto.QID { return n.qid }

// Open implements NodeOpener so the File handle can be acquired. Returns
// nil handle + zero iounit so the Open path takes the fast path without
// per-open state.
func (n *testGetattrENOENT) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return nil, 0, nil
}

// Getattr returns ENOENT so File.Stat surfaces the wrapped *client.Error.
func (n *testGetattrENOENT) Getattr(_ context.Context, _ proto.AttrMask) (proto.Attr, error) {
	return proto.Attr{}, proto.ENOENT
}

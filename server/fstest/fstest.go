// Package fstest provides a protocol-level test harness for validating
// filesystem implementations against the 9P2000.L contract.
//
// Call Check(t, root) to run the standard test suite against any root
// Node. The root must contain the following tree shape:
//
//	root/
//	  file.txt  (content: "hello world")
//	  empty     (content: "")
//	  sub/
//	    nested.txt (content: "nested content")
//
// Cases is the exported slice of all test cases, enabling selective
// execution via Cases[i].Run(t, root) or filtering by name prefix.
package fstest

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
	"github.com/dotwaffle/ninep/server"
)

// TestCase defines a single protocol-level test against a filesystem root.
type TestCase struct {
	Name string
	Run  func(t *testing.T, root server.Node)
}

// Cases holds all registered test cases. Populated by init in cases.go.
var Cases []TestCase

// ExpectedTree documents the tree shape that root must contain for Check
// to pass. Keys are slash-separated paths relative to root; values are
// expected file contents.
var ExpectedTree = map[string]string{
	"file.txt":       "hello world",
	"empty":          "",
	"sub/nested.txt": "nested content",
}

// Check runs every registered test case against root as a subtest.
// The root node is shared across all test cases, which works for
// implementations without destructible state (e.g., memfs). For
// implementations with OS-level resources (e.g., passthrough), use
// CheckFactory instead.
func Check(t *testing.T, root server.Node) {
	t.Helper()
	CheckFactory(t, func(_ *testing.T) server.Node { return root })
}

// CheckFactory runs every registered test case, calling newRoot for each
// case to obtain a fresh root node. This is necessary for filesystem
// implementations like passthrough where the server's cleanup closes
// OS-level resources on the root.
func CheckFactory(t *testing.T, newRoot func(t *testing.T) server.Node) {
	t.Helper()
	for _, tc := range Cases {
		t.Run(tc.Name, func(t *testing.T) {
			root := newRoot(t)
			tc.Run(t, root)
		})
	}
}

// testConn wraps a net.Pipe-backed server connection with version
// negotiation already completed. All protocol-level test helpers
// operate through testConn.
type testConn struct {
	client net.Conn
	done   chan struct{}
	cancel context.CancelFunc
}

// newTestConn creates a server serving root over a net.Pipe, negotiates
// the 9P2000.L version, and returns a testConn ready for protocol
// operations. A 5-second timeout prevents test hangs from broken
// implementations (T-06-10 mitigation).
func newTestConn(t *testing.T, root server.Node) *testConn {
	t.Helper()

	discardLog := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := server.New(root, server.WithMaxMsize(65536), server.WithLogger(discardLog))

	client, srvConn := net.Pipe()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		_ = srvConn.Close()
		<-done // Wait for server goroutine to finish cleanup.
	})
	go func() {
		defer close(done)
		srv.ServeConn(ctx, srvConn)
	}()

	// Version negotiation.
	sendMsg(t, client, proto.NoTag, &proto.Tversion{Msize: 65536, Version: "9P2000.L"})
	_, msg := readMsg(t, client)
	rv, ok := msg.(*proto.Rversion)
	if !ok {
		t.Fatalf("expected Rversion, got %T: %+v", msg, msg)
	}
	if rv.Version != "9P2000.L" {
		t.Fatalf("version negotiation failed: got %q", rv.Version)
	}

	return &testConn{client: client, done: done, cancel: cancel}
}

// sendMsg encodes a full 9P2000.L message to w.
func sendMsg(t *testing.T, w io.Writer, tag proto.Tag, msg proto.Message) {
	t.Helper()
	if err := p9l.Encode(w, tag, msg); err != nil {
		t.Fatalf("encode %s: %v", msg.Type(), err)
	}
}

// readMsg reads a full 9P2000.L message from r.
func readMsg(t *testing.T, r io.Reader) (proto.Tag, proto.Message) {
	t.Helper()
	tag, msg, err := p9l.Decode(r)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return tag, msg
}

// attach sends Tattach and returns the Rattach. Fatal on error.
func attach(t *testing.T, tc *testConn, tag proto.Tag, fid proto.Fid, uname, aname string) *proto.Rattach {
	t.Helper()
	sendMsg(t, tc.client, tag, &proto.Tattach{
		Fid:   fid,
		Afid:  proto.NoFid,
		Uname: uname,
		Aname: aname,
	})
	_, msg := readMsg(t, tc.client)
	ra, ok := msg.(*proto.Rattach)
	if !ok {
		t.Fatalf("expected Rattach, got %T: %+v", msg, msg)
	}
	return ra
}

// walk sends Twalk and returns the raw response (Rwalk or Rlerror).
func walk(t *testing.T, tc *testConn, tag proto.Tag, fid, newFid proto.Fid, names ...string) proto.Message {
	t.Helper()
	sendMsg(t, tc.client, tag, &proto.Twalk{
		Fid:    fid,
		NewFid: newFid,
		Names:  names,
	})
	_, msg := readMsg(t, tc.client)
	return msg
}

// open sends Tlopen and returns the raw response (Rlopen or Rlerror).
func open(t *testing.T, tc *testConn, tag proto.Tag, fid proto.Fid, flags uint32) proto.Message {
	t.Helper()
	sendMsg(t, tc.client, tag, &p9l.Tlopen{
		Fid:   fid,
		Flags: flags,
	})
	_, msg := readMsg(t, tc.client)
	return msg
}

// read sends Tread and returns the raw response (Rread or Rlerror).
func read(t *testing.T, tc *testConn, tag proto.Tag, fid proto.Fid, offset uint64, count uint32) proto.Message {
	t.Helper()
	sendMsg(t, tc.client, tag, &proto.Tread{
		Fid:    fid,
		Offset: offset,
		Count:  count,
	})
	_, msg := readMsg(t, tc.client)
	return msg
}

// write sends Twrite and returns the raw response (Rwrite or Rlerror).
func write(t *testing.T, tc *testConn, tag proto.Tag, fid proto.Fid, offset uint64, data []byte) proto.Message {
	t.Helper()
	sendMsg(t, tc.client, tag, &proto.Twrite{
		Fid:    fid,
		Offset: offset,
		Data:   data,
	})
	_, msg := readMsg(t, tc.client)
	return msg
}

// clunk sends Tclunk and returns the raw response.
func clunk(t *testing.T, tc *testConn, tag proto.Tag, fid proto.Fid) proto.Message {
	t.Helper()
	sendMsg(t, tc.client, tag, &proto.Tclunk{Fid: fid})
	_, msg := readMsg(t, tc.client)
	return msg
}

// create sends Tlcreate and returns the raw response.
func create(t *testing.T, tc *testConn, tag proto.Tag, fid proto.Fid, name string, flags uint32, mode proto.FileMode, gid uint32) proto.Message {
	t.Helper()
	sendMsg(t, tc.client, tag, &p9l.Tlcreate{
		Fid:   fid,
		Name:  name,
		Flags: flags,
		Mode:  mode,
		GID:   gid,
	})
	_, msg := readMsg(t, tc.client)
	return msg
}

// mkdir sends Tmkdir and returns the raw response.
func mkdir(t *testing.T, tc *testConn, tag proto.Tag, dirFid proto.Fid, name string, mode proto.FileMode, gid uint32) proto.Message {
	t.Helper()
	sendMsg(t, tc.client, tag, &p9l.Tmkdir{
		DirFid: dirFid,
		Name:   name,
		Mode:   mode,
		GID:    gid,
	})
	_, msg := readMsg(t, tc.client)
	return msg
}

// getattr sends Tgetattr and returns the raw response.
func getattr(t *testing.T, tc *testConn, tag proto.Tag, fid proto.Fid, mask proto.AttrMask) proto.Message {
	t.Helper()
	sendMsg(t, tc.client, tag, &p9l.Tgetattr{
		Fid:         fid,
		RequestMask: mask,
	})
	_, msg := readMsg(t, tc.client)
	return msg
}

// readdir sends Treaddir and returns the raw response.
func readdir(t *testing.T, tc *testConn, tag proto.Tag, fid proto.Fid, offset uint64, count uint32) proto.Message {
	t.Helper()
	sendMsg(t, tc.client, tag, &p9l.Treaddir{
		Fid:    fid,
		Offset: offset,
		Count:  count,
	})
	_, msg := readMsg(t, tc.client)
	return msg
}

// unlink sends Tunlinkat and returns the raw response.
func unlink(t *testing.T, tc *testConn, tag proto.Tag, dirFid proto.Fid, name string, flags uint32) proto.Message {
	t.Helper()
	sendMsg(t, tc.client, tag, &p9l.Tunlinkat{
		DirFid: dirFid,
		Name:   name,
		Flags:  flags,
	})
	_, msg := readMsg(t, tc.client)
	return msg
}

// expectRwalk asserts the response is an Rwalk and returns it.
func expectRwalk(t *testing.T, msg proto.Message) *proto.Rwalk {
	t.Helper()
	rw, ok := msg.(*proto.Rwalk)
	if !ok {
		t.Fatalf("expected Rwalk, got %T: %+v", msg, msg)
	}
	return rw
}

// expectRlerror asserts the response is an Rlerror with the expected errno.
func expectRlerror(t *testing.T, msg proto.Message, want proto.Errno) {
	t.Helper()
	rlerr, ok := msg.(*p9l.Rlerror)
	if !ok {
		t.Fatalf("expected Rlerror, got %T: %+v", msg, msg)
	}
	if rlerr.Ecode != want {
		t.Errorf("errno = %d (%s), want %d (%s)", rlerr.Ecode, rlerr.Ecode, want, want)
	}
}

// expectRread asserts the response is an Rread and returns the data.
func expectRread(t *testing.T, msg proto.Message) []byte {
	t.Helper()
	rr, ok := msg.(*proto.Rread)
	if !ok {
		t.Fatalf("expected Rread, got %T: %+v", msg, msg)
	}
	return rr.Data
}

// expectRwrite asserts the response is an Rwrite and returns the count.
func expectRwrite(t *testing.T, msg proto.Message) uint32 {
	t.Helper()
	rw, ok := msg.(*proto.Rwrite)
	if !ok {
		t.Fatalf("expected Rwrite, got %T: %+v", msg, msg)
	}
	return rw.Count
}

// NewTestTree constructs the standard test tree using memfs for use with
// Check. The tree layout matches ExpectedTree:
//
//	root/
//	  file.txt  (content: "hello world")
//	  empty     (content: "")
//	  sub/
//	    nested.txt (content: "nested content")
//
// This function is exported so users can create test trees for their own
// test suites. It requires the memfs package, but is defined here as a
// convenience. Users may also construct their own root Node matching the
// expected layout.
//
// Note: This uses the memfs builder API. Import is via the exported
// function to avoid circular dependencies.
func NewTestTree(gen *server.QIDGenerator) server.Node {
	// Build using low-level Inode API instead of builder to avoid import
	// of memfs (which would create a test dependency that this package
	// should not impose).
	root := &testDir{gen: gen}
	root.Init(gen.Next(proto.QTDIR), root)

	fileTxt := &testFile{data: []byte("hello world")}
	fileTxt.Init(gen.Next(proto.QTFILE), fileTxt)
	root.AddChild("file.txt", fileTxt.EmbeddedInode())

	empty := &testFile{data: []byte("")}
	empty.Init(gen.Next(proto.QTFILE), empty)
	root.AddChild("empty", empty.EmbeddedInode())

	sub := &testDir{gen: gen}
	sub.Init(gen.Next(proto.QTDIR), sub)
	root.AddChild("sub", sub.EmbeddedInode())

	nested := &testFile{data: []byte("nested content")}
	nested.Init(gen.Next(proto.QTFILE), nested)
	sub.AddChild("nested.txt", nested.EmbeddedInode())

	return root
}

// testDir is an in-memory directory for test tree construction.
type testDir struct {
	server.Inode
	gen *server.QIDGenerator
}

func (d *testDir) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return nil, 0, nil
}

func (d *testDir) Readdir(_ context.Context) ([]proto.Dirent, error) {
	children := d.Children()
	entries := make([]proto.Dirent, 0, len(children))
	var offset uint64
	for name, inode := range children {
		qid := inode.QID()
		offset++
		entries = append(entries, proto.Dirent{
			QID:    qid,
			Offset: offset,
			Type:   uint8(qid.Type),
			Name:   name,
		})
	}
	return entries, nil
}

func (d *testDir) Getattr(_ context.Context, _ proto.AttrMask) (proto.Attr, error) {
	children := d.Children()
	return proto.Attr{
		Mode:  0o040755,
		NLink: uint64(2 + len(children)),
	}, nil
}

func (d *testDir) Create(_ context.Context, name string, _ uint32, mode proto.FileMode, _ uint32) (server.Node, server.FileHandle, uint32, error) {
	child := &testFile{data: nil}
	child.Init(d.gen.Next(proto.QTFILE), child)
	d.AddChild(name, child.EmbeddedInode())
	return child, nil, 0, nil
}

func (d *testDir) Mkdir(_ context.Context, name string, _ proto.FileMode, _ uint32) (server.Node, error) {
	child := &testDir{gen: d.gen}
	child.Init(d.gen.Next(proto.QTDIR), child)
	d.AddChild(name, child.EmbeddedInode())
	return child, nil
}

func (d *testDir) Unlink(_ context.Context, name string, _ uint32) error {
	d.EmbeddedInode().RemoveChild(name)
	return nil
}

// testFile is an in-memory file for test tree construction.
type testFile struct {
	server.Inode
	data []byte
}

func (f *testFile) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return nil, 0, nil
}

func (f *testFile) Read(_ context.Context, offset uint64, count uint32) ([]byte, error) {
	size := uint64(len(f.data))
	if offset >= size {
		return nil, nil
	}
	end := offset + uint64(count)
	if end > size {
		end = size
	}
	out := make([]byte, end-offset)
	copy(out, f.data[offset:end])
	return out, nil
}

func (f *testFile) Write(_ context.Context, data []byte, offset uint64) (uint32, error) {
	end := int(offset) + len(data)
	if end > len(f.data) {
		newData := make([]byte, end)
		copy(newData, f.data)
		f.data = newData
	}
	copy(f.data[offset:], data)
	return uint32(len(data)), nil
}

func (f *testFile) Getattr(_ context.Context, _ proto.AttrMask) (proto.Attr, error) {
	return proto.Attr{
		Mode:  0o644,
		Size:  uint64(len(f.data)),
		NLink: 1,
	}, nil
}

// parseDirents parses raw Rreaddir data into a list of dirent entries.
// Wire format per entry: qid[13] offset[8] type[1] name[s].
func parseDirents(data []byte) []proto.Dirent {
	r := bytes.NewReader(data)
	var dirents []proto.Dirent
	for r.Len() > 0 {
		qid, err := proto.ReadQID(r)
		if err != nil {
			break
		}
		offset, err := proto.ReadUint64(r)
		if err != nil {
			break
		}
		dtype, err := proto.ReadUint8(r)
		if err != nil {
			break
		}
		name, err := proto.ReadString(r)
		if err != nil {
			break
		}
		dirents = append(dirents, proto.Dirent{
			QID:    qid,
			Offset: offset,
			Type:   dtype,
			Name:   name,
		})
	}
	return dirents
}

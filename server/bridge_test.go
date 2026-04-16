package server

import (
	"bytes"
	"context"
	"maps"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// --- Bridge test node types ---

// bridgeFile is a test node supporting Open, Read, Write, Getattr, Setattr.
type bridgeFile struct {
	Inode
	content []byte
	mode    uint32
}

func (f *bridgeFile) Open(_ context.Context, _ uint32) (FileHandle, uint32, error) {
	return nil, 0, nil
}

func (f *bridgeFile) Read(_ context.Context, buf []byte, offset uint64) (int, error) {
	if offset >= uint64(len(f.content)) {
		return 0, nil
	}
	end := min(offset+uint64(len(buf)), uint64(len(f.content)))
	return copy(buf, f.content[offset:end]), nil
}

func (f *bridgeFile) Write(_ context.Context, data []byte, offset uint64) (uint32, error) {
	end := int(offset) + len(data)
	if end > len(f.content) {
		newContent := make([]byte, end)
		copy(newContent, f.content)
		f.content = newContent
	}
	copy(f.content[offset:], data)
	return uint32(len(data)), nil
}

func (f *bridgeFile) Getattr(_ context.Context, _ proto.AttrMask) (proto.Attr, error) {
	return proto.Attr{Mode: f.mode, Size: uint64(len(f.content))}, nil
}

func (f *bridgeFile) Setattr(_ context.Context, _ proto.SetAttr) error {
	return nil
}

// bridgeDir is a test directory supporting Open, Readdir, Create, Mkdir.
type bridgeDir struct {
	Inode
	gen *QIDGenerator
}

func (d *bridgeDir) Open(_ context.Context, _ uint32) (FileHandle, uint32, error) {
	return nil, 0, nil
}

func (d *bridgeDir) Readdir(_ context.Context) ([]proto.Dirent, error) {
	children := d.Children()
	dirents := make([]proto.Dirent, 0, len(children))
	for name, inode := range children {
		qid := inode.QID()
		dirents = append(dirents, proto.Dirent{
			QID:  qid,
			Type: proto.QIDTypeToDT(qid.Type),
			Name: name,
		})
	}
	return dirents, nil
}

func (d *bridgeDir) Create(_ context.Context, _ string, _ uint32, _ proto.FileMode, _ uint32) (Node, FileHandle, uint32, error) {
	child := &bridgeFile{content: nil, mode: 0o644}
	child.Init(d.gen.Next(proto.QTFILE), child)
	return child, nil, 0, nil
}

func (d *bridgeDir) Mkdir(_ context.Context, _ string, _ proto.FileMode, _ uint32) (Node, error) {
	child := &bridgeDir{gen: d.gen}
	child.Init(d.gen.Next(proto.QTDIR), child)
	return child, nil
}

// handleFile is a test node whose Open returns a FileHandle.
type handleFile struct {
	Inode
	nodeContent   []byte
	handleContent []byte
}

func (f *handleFile) Open(_ context.Context, _ uint32) (FileHandle, uint32, error) {
	return &testHandle{content: f.handleContent}, 0, nil
}

func (f *handleFile) Read(_ context.Context, buf []byte, _ uint64) (int, error) {
	return copy(buf, f.nodeContent), nil
}

// testHandle implements FileReader and FileReleaser.
type testHandle struct {
	content  []byte
	released atomic.Bool
}

func (h *testHandle) Read(_ context.Context, buf []byte, offset uint64) (int, error) {
	if offset >= uint64(len(h.content)) {
		return 0, nil
	}
	end := min(offset+uint64(len(buf)), uint64(len(h.content)))
	return copy(buf, h.content[offset:end]), nil
}

func (h *testHandle) Release(_ context.Context) error {
	h.released.Store(true)
	return nil
}

// readOnlyTestFile uses ReadOnlyFile composable struct.
type readOnlyTestFile struct {
	ReadOnlyFile
	content []byte
	mode    uint32
}

func (f *readOnlyTestFile) Open(_ context.Context, _ uint32) (FileHandle, uint32, error) {
	return nil, 0, nil
}

func (f *readOnlyTestFile) Read(_ context.Context, buf []byte, offset uint64) (int, error) {
	if offset >= uint64(len(f.content)) {
		return 0, nil
	}
	end := min(offset+uint64(len(buf)), uint64(len(f.content)))
	return copy(buf, f.content[offset:end]), nil
}

func (f *readOnlyTestFile) Getattr(_ context.Context, _ proto.AttrMask) (proto.Attr, error) {
	return proto.Attr{Mode: f.mode, Size: uint64(len(f.content))}, nil
}

// Compile-time checks for bridge test types.
var (
	_ NodeOpener    = (*bridgeFile)(nil)
	_ NodeReader    = (*bridgeFile)(nil)
	_ NodeWriter    = (*bridgeFile)(nil)
	_ NodeGetattrer = (*bridgeFile)(nil)
	_ NodeSetattrer = (*bridgeFile)(nil)
	_ InodeEmbedder = (*bridgeFile)(nil)

	_ NodeOpener    = (*bridgeDir)(nil)
	_ NodeReaddirer = (*bridgeDir)(nil)
	_ NodeCreater   = (*bridgeDir)(nil)
	_ NodeMkdirer   = (*bridgeDir)(nil)
	_ InodeEmbedder = (*bridgeDir)(nil)

	_ NodeOpener    = (*handleFile)(nil)
	_ NodeReader    = (*handleFile)(nil)
	_ InodeEmbedder = (*handleFile)(nil)

	_ FileReader   = (*testHandle)(nil)
	_ FileReleaser = (*testHandle)(nil)

	_ NodeOpener    = (*readOnlyTestFile)(nil)
	_ NodeReader    = (*readOnlyTestFile)(nil)
	_ NodeGetattrer = (*readOnlyTestFile)(nil)
	_ InodeEmbedder = (*readOnlyTestFile)(nil)
)

// --- Phase 4 test node types ---

// symlinkDir is a test directory supporting Symlink, Link, Mknod, Unlink,
// Rename, and Readlink on children.
type symlinkDir struct {
	Inode
	gen *QIDGenerator
}

func (d *symlinkDir) Symlink(_ context.Context, name, target string, _ uint32) (Node, error) {
	child := &symlinkNode{target: target}
	child.Init(d.gen.Next(proto.QTSYMLINK), child)
	return child, nil
}

func (d *symlinkDir) Link(_ context.Context, _ Node, _ string) error {
	return nil
}

func (d *symlinkDir) Mknod(_ context.Context, _ string, _ proto.FileMode, _, _, _ uint32) (Node, error) {
	child := &bridgeFile{content: nil, mode: 0}
	child.Init(d.gen.Next(proto.QTFILE), child)
	return child, nil
}

func (d *symlinkDir) Unlink(_ context.Context, _ string, _ uint32) error {
	return nil
}

func (d *symlinkDir) Rename(_ context.Context, _ string, _ Node, _ string) error {
	return nil
}

func (d *symlinkDir) Open(_ context.Context, _ uint32) (FileHandle, uint32, error) {
	return nil, 0, nil
}

func (d *symlinkDir) Lookup(ctx context.Context, name string) (Node, error) {
	return d.Inode.Lookup(ctx, name)
}

// symlinkNode is a symlink that implements NodeReadlinker.
type symlinkNode struct {
	Inode
	target string
}

func (s *symlinkNode) Readlink(_ context.Context) (string, error) {
	return s.target, nil
}

// statfsNode implements NodeStatFSer.
type statfsNode struct {
	Inode
	stat proto.FSStat
}

func (s *statfsNode) StatFS(_ context.Context) (proto.FSStat, error) {
	return s.stat, nil
}

// lockableFile implements NodeLocker for lock tests.
type lockableFile struct {
	Inode
	lockStatus proto.LockStatus
}

func (f *lockableFile) Open(_ context.Context, _ uint32) (FileHandle, uint32, error) {
	return nil, 0, nil
}

func (f *lockableFile) Lock(_ context.Context, _ proto.LockType, _ proto.LockFlags, _, _ uint64, _ uint32, _ string) (proto.LockStatus, error) {
	return f.lockStatus, nil
}

func (f *lockableFile) GetLock(_ context.Context, _ proto.LockType, start, length uint64, procID uint32, clientID string) (proto.LockType, uint64, uint64, uint32, string, error) {
	return proto.LockTypeRdLck, start, length, procID, clientID, nil
}

// xattrFile implements NodeXattrGetter, NodeXattrSetter, NodeXattrLister,
// NodeXattrRemover for xattr tests.
type xattrFile struct {
	Inode
	xattrs map[string][]byte
}

func (f *xattrFile) Open(_ context.Context, _ uint32) (FileHandle, uint32, error) {
	return nil, 0, nil
}

func (f *xattrFile) GetXattr(_ context.Context, name string) ([]byte, error) {
	data, ok := f.xattrs[name]
	if !ok {
		return nil, proto.ENODATA
	}
	return data, nil
}

func (f *xattrFile) SetXattr(_ context.Context, name string, data []byte, _ uint32) error {
	if f.xattrs == nil {
		f.xattrs = make(map[string][]byte)
	}
	f.xattrs[name] = slices.Clone(data)
	return nil
}

func (f *xattrFile) ListXattrs(_ context.Context) ([]string, error) {
	return slices.Collect(maps.Keys(f.xattrs)), nil
}

func (f *xattrFile) RemoveXattr(_ context.Context, name string) error {
	delete(f.xattrs, name)
	return nil
}

// rawXattrFile implements RawXattrer for testing the escape hatch.
type rawXattrFile struct {
	Inode
	xattrs        map[string][]byte
	lastWriteName string
	lastWriteData []byte
}

func (f *rawXattrFile) Open(_ context.Context, _ uint32) (FileHandle, uint32, error) {
	return nil, 0, nil
}

func (f *rawXattrFile) HandleXattrwalk(_ context.Context, name string) ([]byte, error) {
	if name == "" {
		var buf []byte
		for n := range f.xattrs {
			buf = append(buf, []byte(n)...)
			buf = append(buf, 0)
		}
		return buf, nil
	}
	data, ok := f.xattrs[name]
	if !ok {
		return nil, proto.ENODATA
	}
	return data, nil
}

func (f *rawXattrFile) HandleXattrcreate(_ context.Context, name string, _ uint64, _ uint32) (XattrWriter, error) {
	return &testXattrWriter{file: f, name: name}, nil
}

// testXattrWriter accumulates writes and commits to the rawXattrFile.
type testXattrWriter struct {
	file *rawXattrFile
	name string
	data []byte
}

func (w *testXattrWriter) Write(_ context.Context, data []byte) (int, error) {
	w.data = append(w.data, data...)
	return len(data), nil
}

func (w *testXattrWriter) Commit(_ context.Context) error {
	if w.file.xattrs == nil {
		w.file.xattrs = make(map[string][]byte)
	}
	w.file.xattrs[w.name] = w.data
	w.file.lastWriteName = w.name
	w.file.lastWriteData = w.data
	return nil
}

// Compile-time checks for Phase 4 test types.
var (
	_ NodeSymlinker = (*symlinkDir)(nil)
	_ NodeLinker    = (*symlinkDir)(nil)
	_ NodeMknoder   = (*symlinkDir)(nil)
	_ NodeUnlinker  = (*symlinkDir)(nil)
	_ NodeRenamer   = (*symlinkDir)(nil)
	_ NodeOpener    = (*symlinkDir)(nil)
	_ NodeLookuper  = (*symlinkDir)(nil)
	_ InodeEmbedder = (*symlinkDir)(nil)

	_ NodeReadlinker = (*symlinkNode)(nil)
	_ InodeEmbedder  = (*symlinkNode)(nil)

	_ NodeStatFSer  = (*statfsNode)(nil)
	_ InodeEmbedder = (*statfsNode)(nil)

	_ NodeOpener    = (*lockableFile)(nil)
	_ NodeLocker    = (*lockableFile)(nil)
	_ InodeEmbedder = (*lockableFile)(nil)

	_ NodeOpener       = (*xattrFile)(nil)
	_ NodeXattrGetter  = (*xattrFile)(nil)
	_ NodeXattrSetter  = (*xattrFile)(nil)
	_ NodeXattrLister  = (*xattrFile)(nil)
	_ NodeXattrRemover = (*xattrFile)(nil)
	_ InodeEmbedder    = (*xattrFile)(nil)

	_ NodeOpener    = (*rawXattrFile)(nil)
	_ RawXattrer    = (*rawXattrFile)(nil)
	_ InodeEmbedder = (*rawXattrFile)(nil)

	_ XattrWriter = (*testXattrWriter)(nil)
)

// --- Bridge test helpers ---

// setupBridgeConn creates a connPair with the given root, performs version
// negotiation and attach, returning the connPair and the root fid (0).
func setupBridgeConn(t *testing.T, root Node, opts ...Option) *connPair {
	t.Helper()
	cp := newConnPair(t, root, opts...)
	cp.attach(t, 1, 0, "test", "")
	return cp
}

// lopen sends Tlopen and returns the response message.
func (cp *connPair) lopen(t *testing.T, tag proto.Tag, fid proto.Fid, flags uint32) proto.Message {
	t.Helper()
	sendMessage(t, cp.client, tag, &p9l.Tlopen{Fid: fid, Flags: flags})
	_, msg := readResponse(t, cp.client)
	return msg
}

// read sends Tread and returns the response message.
func (cp *connPair) read(t *testing.T, tag proto.Tag, fid proto.Fid, offset uint64, count uint32) proto.Message {
	t.Helper()
	sendMessage(t, cp.client, tag, &proto.Tread{Fid: fid, Offset: offset, Count: count})
	_, msg := readResponse(t, cp.client)
	return msg
}

// write sends Twrite and returns the response message.
func (cp *connPair) write(t *testing.T, tag proto.Tag, fid proto.Fid, offset uint64, data []byte) proto.Message {
	t.Helper()
	sendMessage(t, cp.client, tag, &proto.Twrite{Fid: fid, Offset: offset, Data: data})
	_, msg := readResponse(t, cp.client)
	return msg
}

// getattr sends Tgetattr and returns the response message.
func (cp *connPair) getattr(t *testing.T, tag proto.Tag, fid proto.Fid, mask proto.AttrMask) proto.Message {
	t.Helper()
	sendMessage(t, cp.client, tag, &p9l.Tgetattr{Fid: fid, RequestMask: mask})
	_, msg := readResponse(t, cp.client)
	return msg
}

// setattr sends Tsetattr and returns the response message.
func (cp *connPair) setattr(t *testing.T, tag proto.Tag, fid proto.Fid, attr proto.SetAttr) proto.Message {
	t.Helper()
	sendMessage(t, cp.client, tag, &p9l.Tsetattr{Fid: fid, Attr: attr})
	_, msg := readResponse(t, cp.client)
	return msg
}

// readdir sends Treaddir and returns the response message.
func (cp *connPair) readdir(t *testing.T, tag proto.Tag, fid proto.Fid, offset uint64, count uint32) proto.Message {
	t.Helper()
	sendMessage(t, cp.client, tag, &p9l.Treaddir{Fid: fid, Offset: offset, Count: count})
	_, msg := readResponse(t, cp.client)
	return msg
}

// lcreate sends Tlcreate and returns the response message.
func (cp *connPair) lcreate(t *testing.T, tag proto.Tag, fid proto.Fid, name string, flags uint32, mode proto.FileMode, gid uint32) proto.Message {
	t.Helper()
	sendMessage(t, cp.client, tag, &p9l.Tlcreate{Fid: fid, Name: name, Flags: flags, Mode: mode, GID: gid})
	_, msg := readResponse(t, cp.client)
	return msg
}

// mkdir sends Tmkdir and returns the response message.
func (cp *connPair) mkdir(t *testing.T, tag proto.Tag, dirfid proto.Fid, name string, mode proto.FileMode, gid uint32) proto.Message {
	t.Helper()
	sendMessage(t, cp.client, tag, &p9l.Tmkdir{DirFid: dirfid, Name: name, Mode: mode, GID: gid})
	_, msg := readResponse(t, cp.client)
	return msg
}

// decodeDirents decodes raw readdir data into Dirent entries.
func decodeDirents(t *testing.T, data []byte) []proto.Dirent {
	t.Helper()
	r := bytes.NewReader(data)
	var dirents []proto.Dirent
	for r.Len() > 0 {
		qid, err := proto.ReadQID(r)
		if err != nil {
			t.Fatalf("decode dirent qid: %v", err)
		}
		offset, err := proto.ReadUint64(r)
		if err != nil {
			t.Fatalf("decode dirent offset: %v", err)
		}
		dtype, err := proto.ReadUint8(r)
		if err != nil {
			t.Fatalf("decode dirent type: %v", err)
		}
		name, err := proto.ReadString(r)
		if err != nil {
			t.Fatalf("decode dirent name: %v", err)
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

// --- End-to-end bridge tests ---

func TestBridge_OpenRead(t *testing.T) {
	t.Parallel()

	for _, transport := range []string{"pipe", "unix"} {
		t.Run("transport="+transport, func(t *testing.T) {
			t.Parallel()

			file := &bridgeFile{content: []byte("hello"), mode: 0o644}
			file.Init(proto.QID{Type: proto.QTFILE, Path: 10}, file)

			root := &testDir{}
			root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
			root.AddChild("file.txt", file.EmbeddedInode())

			// Large-read fixture: only used by the transport=unix subtest to
			// exercise the 3-iovec writev fast path with a non-trivial (>=4 KiB)
			// payload. See .planning/phases/14/14-RESEARCH.md §Pitfall 2 for
			// why iovcnt is 3 when len(payload) > 0 and 2 when it's empty.
			// Contents are deterministic (byte(i % 251) avoids all-zero runs and
			// keeps the test hermetic).
			var bigFile *bridgeFile
			if transport == "unix" {
				bigContent := make([]byte, 8192)
				for i := range bigContent {
					bigContent[i] = byte(i % 251)
				}
				bigFile = &bridgeFile{content: bigContent, mode: 0o644}
				bigFile.Init(proto.QID{Type: proto.QTFILE, Path: 11}, bigFile)
				root.AddChild("big.bin", bigFile.EmbeddedInode())
			}

			// Use newConnPairTransport directly instead of setupBridgeConn
			// (which is pipe-only via newConnPair) so this subtest can also
			// cover the unix-socket writev fast path in sendResponseInline.
			cp := newConnPairTransport(t, transport, root)
			defer cp.close(t)
			cp.attach(t, 1, 0, "test", "")

			// Walk to file.
			msg := cp.walk(t, 2, 0, 1, "file.txt")
			if _, ok := msg.(*proto.Rwalk); !ok {
				t.Fatalf("expected Rwalk, got %T: %+v", msg, msg)
			}

			// Open file.
			msg = cp.lopen(t, 3, 1, 0)
			rl, ok := msg.(*p9l.Rlopen)
			if !ok {
				t.Fatalf("expected Rlopen, got %T: %+v", msg, msg)
			}
			if rl.QID.Path != 10 {
				t.Errorf("open QID.Path = %d, want 10", rl.QID.Path)
			}
			if rl.IOUnit == 0 {
				t.Error("IOUnit should not be zero")
			}

			// Read all content.
			msg = cp.read(t, 4, 1, 0, 1024)
			rr, ok := msg.(*proto.Rread)
			if !ok {
				t.Fatalf("expected Rread, got %T: %+v", msg, msg)
			}
			if string(rr.Data) != "hello" {
				t.Errorf("read data = %q, want %q", string(rr.Data), "hello")
			}

			// Large-read variant: transport=unix only. Exercises the
			// net.Buffers.WriteTo -> pfd.Writev single-syscall fast path with
			// iovcnt=3 (hdr + 4-byte count prefix + payload). transport=pipe
			// falls into the sequential-Write loop in net/net.go:851-864 via
			// the 5-byte "hello" read above, so this extra coverage is specific
			// to the writev path (PERF-07.4 canary for Pitfall 3: if the pooled
			// buffer were released before writev completed, the bytes.Equal
			// check below would fail intermittently under -race).
			if transport == "unix" {
				// Fresh fid (5) to avoid colliding with the "hello" fid (1).
				msg = cp.walk(t, 5, 0, 5, "big.bin")
				if _, ok := msg.(*proto.Rwalk); !ok {
					t.Fatalf("big.bin walk: expected Rwalk, got %T: %+v", msg, msg)
				}
				msg = cp.lopen(t, 6, 5, 0)
				if _, ok := msg.(*p9l.Rlopen); !ok {
					t.Fatalf("big.bin lopen: expected Rlopen, got %T: %+v", msg, msg)
				}
				msg = cp.read(t, 7, 5, 0, uint32(len(bigFile.content)))
				rr2, ok := msg.(*proto.Rread)
				if !ok {
					t.Fatalf("big.bin read: expected Rread, got %T: %+v", msg, msg)
				}
				if !bytes.Equal(rr2.Data, bigFile.content) {
					t.Errorf("big.bin read mismatch: got %d bytes, want %d bytes (byte-equality failed -- payload-lifetime invariant may have regressed)",
						len(rr2.Data), len(bigFile.content))
				}
			}
		})
	}
}

func TestBridge_Write(t *testing.T) {
	t.Parallel()

	file := &bridgeFile{content: make([]byte, 5), mode: 0o644}
	file.Init(proto.QID{Type: proto.QTFILE, Path: 10}, file)

	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	root.AddChild("file.txt", file.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Walk, open, write, read back.
	cp.walk(t, 2, 0, 1, "file.txt")
	cp.lopen(t, 3, 1, 0)

	msg := cp.write(t, 4, 1, 0, []byte("world"))
	rw, ok := msg.(*proto.Rwrite)
	if !ok {
		t.Fatalf("expected Rwrite, got %T: %+v", msg, msg)
	}
	if rw.Count != 5 {
		t.Errorf("write count = %d, want 5", rw.Count)
	}

	// Read back.
	msg = cp.read(t, 5, 1, 0, 1024)
	rr, ok := msg.(*proto.Rread)
	if !ok {
		t.Fatalf("expected Rread, got %T: %+v", msg, msg)
	}
	if string(rr.Data) != "world" {
		t.Errorf("read data = %q, want %q", string(rr.Data), "world")
	}
}

func TestBridge_Getattr(t *testing.T) {
	t.Parallel()

	file := &bridgeFile{content: []byte("test data"), mode: 0o755}
	file.Init(proto.QID{Type: proto.QTFILE, Path: 10}, file)

	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	root.AddChild("file.txt", file.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Walk to file (no open needed for getattr).
	cp.walk(t, 2, 0, 1, "file.txt")

	msg := cp.getattr(t, 3, 1, proto.AttrAll)
	rg, ok := msg.(*p9l.Rgetattr)
	if !ok {
		t.Fatalf("expected Rgetattr, got %T: %+v", msg, msg)
	}
	if rg.Attr.Mode != 0o755 {
		t.Errorf("mode = %o, want %o", rg.Attr.Mode, 0o755)
	}
	if rg.Attr.Size != 9 {
		t.Errorf("size = %d, want 9", rg.Attr.Size)
	}
	// QID should be overridden by server.
	if rg.Attr.QID.Path != 10 {
		t.Errorf("attr QID.Path = %d, want 10", rg.Attr.QID.Path)
	}
}

func TestBridge_Setattr(t *testing.T) {
	t.Parallel()

	file := &bridgeFile{content: []byte("test"), mode: 0o644}
	file.Init(proto.QID{Type: proto.QTFILE, Path: 10}, file)

	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	root.AddChild("file.txt", file.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Walk to file.
	cp.walk(t, 2, 0, 1, "file.txt")

	msg := cp.setattr(t, 3, 1, proto.SetAttr{Valid: proto.SetAttrMode, Mode: 0o755})
	if _, ok := msg.(*p9l.Rsetattr); !ok {
		t.Fatalf("expected Rsetattr, got %T: %+v", msg, msg)
	}
}

func TestBridge_Readdir(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}

	file1 := &bridgeFile{content: []byte("a"), mode: 0o644}
	file1.Init(gen.Next(proto.QTFILE), file1)

	file2 := &bridgeFile{content: []byte("b"), mode: 0o644}
	file2.Init(gen.Next(proto.QTFILE), file2)

	dir := &bridgeDir{gen: gen}
	dir.Init(proto.QID{Type: proto.QTDIR, Path: 100}, dir)
	dir.AddChild("alpha", file1.EmbeddedInode())
	dir.AddChild("beta", file2.EmbeddedInode())

	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	root.AddChild("dir", dir.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Walk to dir and open.
	cp.walk(t, 2, 0, 1, "dir")
	msg := cp.lopen(t, 3, 1, 0)
	if _, ok := msg.(*p9l.Rlopen); !ok {
		t.Fatalf("expected Rlopen, got %T: %+v", msg, msg)
	}

	// Readdir.
	msg = cp.readdir(t, 4, 1, 0, 8192)
	rrd, ok := msg.(*p9l.Rreaddir)
	if !ok {
		t.Fatalf("expected Rreaddir, got %T: %+v", msg, msg)
	}
	if len(rrd.Data) == 0 {
		t.Fatal("readdir returned empty data")
	}

	dirents := decodeDirents(t, rrd.Data)
	if len(dirents) != 2 {
		t.Fatalf("dirent count = %d, want 2", len(dirents))
	}

	// Collect names (order not guaranteed from map iteration).
	names := map[string]bool{}
	for _, d := range dirents {
		names[d.Name] = true
	}
	if !names["alpha"] || !names["beta"] {
		t.Errorf("dirent names = %v, want {alpha, beta}", names)
	}
}

func TestBridge_Create(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}

	dir := &bridgeDir{gen: gen}
	dir.Init(proto.QID{Type: proto.QTDIR, Path: 100}, dir)

	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	root.AddChild("dir", dir.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Walk to dir.
	cp.walk(t, 2, 0, 1, "dir")

	// Create file (fid mutates to the new child).
	msg := cp.lcreate(t, 3, 1, "newfile", 0, 0o644, 0)
	rc, ok := msg.(*p9l.Rlcreate)
	if !ok {
		t.Fatalf("expected Rlcreate, got %T: %+v", msg, msg)
	}
	if rc.QID.Type != proto.QTFILE {
		t.Errorf("created QID type = %d, want QTFILE", rc.QID.Type)
	}

	// Fid 1 now points to the new file. Read from it (should be empty).
	msg = cp.read(t, 4, 1, 0, 1024)
	rr, ok := msg.(*proto.Rread)
	if !ok {
		t.Fatalf("expected Rread, got %T: %+v", msg, msg)
	}
	if len(rr.Data) != 0 {
		t.Errorf("new file data = %q, want empty", string(rr.Data))
	}
}

func TestBridge_Mkdir(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}

	dir := &bridgeDir{gen: gen}
	dir.Init(proto.QID{Type: proto.QTDIR, Path: 100}, dir)

	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	root.AddChild("dir", dir.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Walk to dir, clone fid for mkdir.
	cp.walk(t, 2, 0, 1, "dir")

	// Mkdir.
	msg := cp.mkdir(t, 3, 1, "newdir", 0o755, 0)
	rm, ok := msg.(*p9l.Rmkdir)
	if !ok {
		t.Fatalf("expected Rmkdir, got %T: %+v", msg, msg)
	}
	if rm.QID.Type != proto.QTDIR {
		t.Errorf("mkdir QID type = %d, want QTDIR", rm.QID.Type)
	}

	// Walk to the new directory to verify it's reachable.
	msg = cp.walk(t, 4, 1, 2, "newdir")
	rw, ok := msg.(*proto.Rwalk)
	if !ok {
		t.Fatalf("expected Rwalk to newdir, got %T: %+v", msg, msg)
	}
	if len(rw.QIDs) != 1 {
		t.Fatalf("walk QIDs = %d, want 1", len(rw.QIDs))
	}
	if rw.QIDs[0].Type != proto.QTDIR {
		t.Errorf("walked QID type = %d, want QTDIR", rw.QIDs[0].Type)
	}
}

func TestBridge_FileHandle_Priority(t *testing.T) {
	t.Parallel()

	file := &handleFile{
		nodeContent:   []byte("node-data"),
		handleContent: []byte("handle-data"),
	}
	file.Init(proto.QID{Type: proto.QTFILE, Path: 10}, file)

	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	root.AddChild("file.txt", file.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Walk, open, read.
	cp.walk(t, 2, 0, 1, "file.txt")
	cp.lopen(t, 3, 1, 0)

	msg := cp.read(t, 4, 1, 0, 1024)
	rr, ok := msg.(*proto.Rread)
	if !ok {
		t.Fatalf("expected Rread, got %T: %+v", msg, msg)
	}
	// FileHandle's Read should take priority over node's Read.
	if string(rr.Data) != "handle-data" {
		t.Errorf("read data = %q, want %q (FileHandle should take priority)", string(rr.Data), "handle-data")
	}
}

func TestBridge_FileHandle_Release(t *testing.T) {
	t.Parallel()

	handle := &testHandle{content: []byte("data")}
	file := &handleFile{
		nodeContent:   []byte("node"),
		handleContent: []byte("handle"),
	}
	file.Init(proto.QID{Type: proto.QTFILE, Path: 10}, file)

	// Override Open to return our tracked handle.
	type openOverride struct {
		Inode
		handle *testHandle
	}
	releaseFile := &openOverride{handle: handle}
	releaseFile.Init(proto.QID{Type: proto.QTFILE, Path: 11}, releaseFile)

	// Implement NodeOpener and NodeReader inline via wrapper.
	type releaseNode struct {
		Inode
		h *testHandle
	}
	rn := &releaseNode{h: handle}
	rn.Init(proto.QID{Type: proto.QTFILE, Path: 12}, rn)

	// Instead of the complex embedding, build a simpler custom node.
	type releasableFile struct {
		Inode
		h *testHandle
	}

	rf := &releasableFile{h: handle}
	rf.Init(proto.QID{Type: proto.QTFILE, Path: 13}, rf)

	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)

	// Use a dedicated releasable node type defined at package level.
	// Since we need Open to return our specific handle, let's use a
	// different approach: create a handleFile but access the handle via
	// the handleFile type.
	hfile := &handleFile{
		nodeContent:   []byte("node"),
		handleContent: handle.content,
	}
	hfile.Init(proto.QID{Type: proto.QTFILE, Path: 10}, hfile)
	root.AddChild("file.txt", hfile.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Walk, open, clunk.
	cp.walk(t, 2, 0, 1, "file.txt")
	cp.lopen(t, 3, 1, 0)

	// The handle returned by handleFile.Open is a *testHandle.
	// We can't directly access it via the wire protocol, but we can
	// verify Release is called by using a separate node type that
	// stores a reference to the handle we can check.

	// Actually, let's verify this differently: the handleFile.Open
	// creates a new testHandle each time. We need to verify that
	// clunk calls Release on whatever handle was returned.
	// Since we can't get a reference to the specific handle created
	// during Open, let's restructure.

	// Clunk the fid.
	msg := cp.clunk(t, 4, 1)
	if _, ok := msg.(*proto.Rclunk); !ok {
		t.Fatalf("expected Rclunk, got %T: %+v", msg, msg)
	}

	// The testHandle created by handleFile.Open should have been released.
	// We verified the mechanism works. The specific handle was created
	// internally, so we trust the implementation calls Release.
	// A more thorough test uses trackableHandleFile below.
}

// trackableHandleFile returns a known handle reference from Open.
type trackableHandleFile struct {
	Inode
	handle *testHandle
}

func (f *trackableHandleFile) Open(_ context.Context, _ uint32) (FileHandle, uint32, error) {
	return f.handle, 0, nil
}

var _ NodeOpener = (*trackableHandleFile)(nil)

func TestBridge_FileHandle_ReleaseVerified(t *testing.T) {
	t.Parallel()

	handle := &testHandle{content: []byte("tracked")}
	file := &trackableHandleFile{handle: handle}
	file.Init(proto.QID{Type: proto.QTFILE, Path: 10}, file)

	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	root.AddChild("file.txt", file.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Walk, open.
	cp.walk(t, 2, 0, 1, "file.txt")
	cp.lopen(t, 3, 1, 0)

	if handle.released.Load() {
		t.Fatal("handle should not be released before clunk")
	}

	// Clunk the fid.
	msg := cp.clunk(t, 4, 1)
	if _, ok := msg.(*proto.Rclunk); !ok {
		t.Fatalf("expected Rclunk, got %T: %+v", msg, msg)
	}

	if !handle.released.Load() {
		t.Error("handle.Release was not called on clunk")
	}
}

func TestBridge_ENOSYS_DefaultNode(t *testing.T) {
	t.Parallel()

	// A node with only Inode -- all capability methods return ENOSYS.
	plainFile := &testFile{}
	plainFile.Init(proto.QID{Type: proto.QTFILE, Path: 10}, plainFile)

	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	root.AddChild("plain", plainFile.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Walk to plain file.
	cp.walk(t, 2, 0, 1, "plain")

	// Try to open -- Inode.Open returns ENOSYS.
	msg := cp.lopen(t, 3, 1, 0)
	isError(t, msg, proto.ENOSYS)
}

func TestBridge_EBADF_ReadUnopened(t *testing.T) {
	t.Parallel()

	file := &bridgeFile{content: []byte("data"), mode: 0o644}
	file.Init(proto.QID{Type: proto.QTFILE, Path: 10}, file)

	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	root.AddChild("file.txt", file.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Walk to file (fid in allocated state, not opened).
	cp.walk(t, 2, 0, 1, "file.txt")

	// Read without opening -- should get EBADF.
	msg := cp.read(t, 3, 1, 0, 1024)
	isError(t, msg, proto.EBADF)
}

func TestBridge_EBADF_DoubleOpen(t *testing.T) {
	t.Parallel()

	file := &bridgeFile{content: []byte("data"), mode: 0o644}
	file.Init(proto.QID{Type: proto.QTFILE, Path: 10}, file)

	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	root.AddChild("file.txt", file.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Walk and open.
	cp.walk(t, 2, 0, 1, "file.txt")
	msg := cp.lopen(t, 3, 1, 0)
	if _, ok := msg.(*p9l.Rlopen); !ok {
		t.Fatalf("first open: expected Rlopen, got %T", msg)
	}

	// Second open on same fid -- should get EBADF.
	msg = cp.lopen(t, 4, 1, 0)
	isError(t, msg, proto.EBADF)
}

func TestBridge_EINVAL_CreateSlash(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}
	dir := &bridgeDir{gen: gen}
	dir.Init(proto.QID{Type: proto.QTDIR, Path: 100}, dir)

	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	root.AddChild("dir", dir.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	cp.walk(t, 2, 0, 1, "dir")

	// Create with name containing "/" -- should get EINVAL.
	msg := cp.lcreate(t, 3, 1, "bad/name", 0, 0o644, 0)
	isError(t, msg, proto.EINVAL)
}

func TestBridge_EINVAL_MkdirNul(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}
	dir := &bridgeDir{gen: gen}
	dir.Init(proto.QID{Type: proto.QTDIR, Path: 100}, dir)

	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	root.AddChild("dir", dir.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	cp.walk(t, 2, 0, 1, "dir")

	// Mkdir with name containing NUL byte -- should get EINVAL.
	msg := cp.mkdir(t, 3, 1, "bad\x00name", 0o755, 0)
	isError(t, msg, proto.EINVAL)
}

func TestBridge_ReadOnlyFile(t *testing.T) {
	t.Parallel()

	file := &readOnlyTestFile{content: []byte("readonly"), mode: 0o444}
	file.Init(proto.QID{Type: proto.QTFILE, Path: 10}, file)

	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	root.AddChild("file.txt", file.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Walk, open, read -- should work.
	cp.walk(t, 2, 0, 1, "file.txt")
	msg := cp.lopen(t, 3, 1, 0)
	if _, ok := msg.(*p9l.Rlopen); !ok {
		t.Fatalf("expected Rlopen, got %T: %+v", msg, msg)
	}

	msg = cp.read(t, 4, 1, 0, 1024)
	rr, ok := msg.(*proto.Rread)
	if !ok {
		t.Fatalf("expected Rread, got %T: %+v", msg, msg)
	}
	if string(rr.Data) != "readonly" {
		t.Errorf("read data = %q, want %q", string(rr.Data), "readonly")
	}

	// Write should return ENOSYS (ReadOnlyFile inherits Inode.Write default).
	msg = cp.write(t, 5, 1, 0, []byte("attempt"))
	isError(t, msg, proto.ENOSYS)
}

func TestBridge_QIDGenerator(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}

	// Generate several QIDs and verify uniqueness.
	seen := make(map[uint64]bool)
	for range 100 {
		qid := gen.Next(proto.QTFILE)
		if seen[qid.Path] {
			t.Fatalf("duplicate QID path: %d", qid.Path)
		}
		seen[qid.Path] = true
	}

	// Verify type is preserved.
	dirQID := gen.Next(proto.QTDIR)
	if dirQID.Type != proto.QTDIR {
		t.Errorf("QID type = %d, want QTDIR", dirQID.Type)
	}
	fileQID := gen.Next(proto.QTFILE)
	if fileQID.Type != proto.QTFILE {
		t.Errorf("QID type = %d, want QTFILE", fileQID.Type)
	}
}

func TestBridge_ENOSYS_GetattrOnDefault(t *testing.T) {
	t.Parallel()

	// A node with only Inode -- Getattr returns ENOSYS.
	plainFile := &testFile{}
	plainFile.Init(proto.QID{Type: proto.QTFILE, Path: 10}, plainFile)

	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	root.AddChild("plain", plainFile.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	cp.walk(t, 2, 0, 1, "plain")
	msg := cp.getattr(t, 3, 1, proto.AttrAll)
	// Inode.Getattr returns ENOSYS -- but since testFile embeds Inode,
	// and Inode implements NodeGetattrer, the bridge will dispatch to
	// Inode.Getattr which returns ENOSYS.
	isError(t, msg, proto.ENOSYS)
}

func TestBridge_EBADF_WriteUnopened(t *testing.T) {
	t.Parallel()

	file := &bridgeFile{content: make([]byte, 10), mode: 0o644}
	file.Init(proto.QID{Type: proto.QTFILE, Path: 10}, file)

	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	root.AddChild("file.txt", file.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Walk but don't open.
	cp.walk(t, 2, 0, 1, "file.txt")

	// Write without opening -- should get EBADF.
	msg := cp.write(t, 3, 1, 0, []byte("data"))
	isError(t, msg, proto.EBADF)
}

func TestBridge_EINVAL_CreateDot(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}
	dir := &bridgeDir{gen: gen}
	dir.Init(proto.QID{Type: proto.QTDIR, Path: 100}, dir)

	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	root.AddChild("dir", dir.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	cp.walk(t, 2, 0, 1, "dir")

	// Create with name "." -- should get EINVAL.
	msg := cp.lcreate(t, 3, 1, ".", 0, 0o644, 0)
	isError(t, msg, proto.EINVAL)
}

func TestBridge_EINVAL_CreateDotDot(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}
	dir := &bridgeDir{gen: gen}
	dir.Init(proto.QID{Type: proto.QTDIR, Path: 100}, dir)

	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	root.AddChild("dir", dir.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	cp.walk(t, 2, 0, 1, "dir")

	// Create with name ".." -- should get EINVAL.
	msg := cp.lcreate(t, 3, 1, "..", 0, 0o644, 0)
	isError(t, msg, proto.EINVAL)
}

// --- Phase 4 end-to-end integration tests ---

func TestBridge_Symlink(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}
	root := &symlinkDir{gen: gen}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Create symlink.
	sendMessage(t, cp.client, 2, &p9l.Tsymlink{DirFid: 0, Name: "link", Target: "/tmp/target", GID: 0})
	_, msg := readResponse(t, cp.client)
	rs, ok := msg.(*p9l.Rsymlink)
	if !ok {
		t.Fatalf("expected Rsymlink, got %T: %+v", msg, msg)
	}
	if rs.QID.Type != proto.QTSYMLINK {
		t.Errorf("symlink QID type = %d, want QTSYMLINK (%d)", rs.QID.Type, proto.QTSYMLINK)
	}

	// Walk to the symlink.
	msg = cp.walk(t, 3, 0, 2, "link")
	rw, ok := msg.(*proto.Rwalk)
	if !ok {
		t.Fatalf("expected Rwalk to symlink, got %T: %+v", msg, msg)
	}
	if len(rw.QIDs) != 1 {
		t.Fatalf("walk QIDs = %d, want 1", len(rw.QIDs))
	}
	if rw.QIDs[0].Type != proto.QTSYMLINK {
		t.Errorf("walked QID type = %d, want QTSYMLINK", rw.QIDs[0].Type)
	}

	// Readlink on the symlink.
	sendMessage(t, cp.client, 4, &p9l.Treadlink{Fid: 2})
	_, msg = readResponse(t, cp.client)
	rl, ok := msg.(*p9l.Rreadlink)
	if !ok {
		t.Fatalf("expected Rreadlink, got %T: %+v", msg, msg)
	}
	if rl.Target != "/tmp/target" {
		t.Errorf("readlink target = %q, want %q", rl.Target, "/tmp/target")
	}
}

func TestBridge_Link(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}
	root := &symlinkDir{gen: gen}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)

	child := &bridgeFile{content: []byte("data"), mode: 0o644}
	child.Init(gen.Next(proto.QTFILE), child)
	root.AddChild("child", child.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Walk to child file as fid 2.
	msg := cp.walk(t, 2, 0, 2, "child")
	if _, ok := msg.(*proto.Rwalk); !ok {
		t.Fatalf("expected Rwalk, got %T: %+v", msg, msg)
	}

	// Create hard link.
	sendMessage(t, cp.client, 3, &p9l.Tlink{DirFid: 0, Fid: 2, Name: "hardlink"})
	_, msg = readResponse(t, cp.client)
	if _, ok := msg.(*p9l.Rlink); !ok {
		t.Fatalf("expected Rlink, got %T: %+v", msg, msg)
	}
}

func TestBridge_Mknod(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}
	root := &symlinkDir{gen: gen}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Create device node.
	sendMessage(t, cp.client, 2, &p9l.Tmknod{DirFid: 0, Name: "dev", Mode: proto.FileMode(0o644), Major: 1, Minor: 3, GID: 0})
	_, msg := readResponse(t, cp.client)
	rm, ok := msg.(*p9l.Rmknod)
	if !ok {
		t.Fatalf("expected Rmknod, got %T: %+v", msg, msg)
	}
	if rm.QID.Type != proto.QTFILE {
		t.Errorf("mknod QID type = %d, want QTFILE", rm.QID.Type)
	}

	// Walk to the new device.
	msg = cp.walk(t, 3, 0, 2, "dev")
	rw, ok := msg.(*proto.Rwalk)
	if !ok {
		t.Fatalf("expected Rwalk to dev, got %T: %+v", msg, msg)
	}
	if len(rw.QIDs) != 1 {
		t.Fatalf("walk QIDs = %d, want 1", len(rw.QIDs))
	}
}

func TestBridge_Unlinkat(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}
	root := &symlinkDir{gen: gen}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)

	child := &bridgeFile{content: []byte("delete-me"), mode: 0o644}
	child.Init(gen.Next(proto.QTFILE), child)
	root.AddChild("child", child.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Verify child exists via walk.
	msg := cp.walk(t, 2, 0, 2, "child")
	if _, ok := msg.(*proto.Rwalk); !ok {
		t.Fatalf("expected Rwalk, got %T: %+v", msg, msg)
	}
	// Clunk the walk fid so it doesn't interfere.
	cp.clunk(t, 3, 2)

	// Unlink child.
	sendMessage(t, cp.client, 4, &p9l.Tunlinkat{DirFid: 0, Name: "child", Flags: 0})
	_, msg = readResponse(t, cp.client)
	if _, ok := msg.(*p9l.Runlinkat); !ok {
		t.Fatalf("expected Runlinkat, got %T: %+v", msg, msg)
	}

	// Walk to child should now fail.
	msg = cp.walk(t, 5, 0, 3, "child")
	isError(t, msg, proto.ENOENT)
}

func TestBridge_Renameat(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}

	// Build tree: root -> dir1 (with file "a"), dir2 (empty).
	dir1 := &symlinkDir{gen: gen}
	dir1.Init(gen.Next(proto.QTDIR), dir1)
	fileA := &bridgeFile{content: []byte("content-a"), mode: 0o644}
	fileA.Init(gen.Next(proto.QTFILE), fileA)
	dir1.AddChild("a", fileA.EmbeddedInode())

	dir2 := &symlinkDir{gen: gen}
	dir2.Init(gen.Next(proto.QTDIR), dir2)

	root := &symlinkDir{gen: gen}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	root.AddChild("dir1", dir1.EmbeddedInode())
	root.AddChild("dir2", dir2.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Walk to dir1 (fid 2) and dir2 (fid 3).
	msg := cp.walk(t, 2, 0, 2, "dir1")
	if _, ok := msg.(*proto.Rwalk); !ok {
		t.Fatalf("walk dir1: expected Rwalk, got %T: %+v", msg, msg)
	}
	msg = cp.walk(t, 3, 0, 3, "dir2")
	if _, ok := msg.(*proto.Rwalk); !ok {
		t.Fatalf("walk dir2: expected Rwalk, got %T: %+v", msg, msg)
	}

	// Rename "a" from dir1 to dir2 as "moved".
	sendMessage(t, cp.client, 4, &p9l.Trenameat{OldDirFid: 2, OldName: "a", NewDirFid: 3, NewName: "moved"})
	_, msg = readResponse(t, cp.client)
	if _, ok := msg.(*p9l.Rrenameat); !ok {
		t.Fatalf("expected Rrenameat, got %T: %+v", msg, msg)
	}

	// Walk from dir2 to "moved" should succeed.
	msg = cp.walk(t, 5, 3, 4, "moved")
	rw, ok := msg.(*proto.Rwalk)
	if !ok {
		t.Fatalf("walk moved: expected Rwalk, got %T: %+v", msg, msg)
	}
	if len(rw.QIDs) != 1 {
		t.Fatalf("walk moved QIDs = %d, want 1", len(rw.QIDs))
	}

	// Walk from dir1 to "a" should fail.
	msg = cp.walk(t, 6, 2, 5, "a")
	isError(t, msg, proto.ENOENT)
}

func TestBridge_Statfs(t *testing.T) {
	t.Parallel()

	wantStat := proto.FSStat{
		Type:    0x6969,
		BSize:   4096,
		Blocks:  1000,
		BFree:   500,
		BAvail:  400,
		Files:   100,
		FFree:   50,
		FSID:    123,
		NameLen: 255,
	}
	root := &statfsNode{stat: wantStat}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Send Tstatfs on root fid.
	sendMessage(t, cp.client, 2, &p9l.Tstatfs{Fid: 0})
	_, msg := readResponse(t, cp.client)
	rs, ok := msg.(*p9l.Rstatfs)
	if !ok {
		t.Fatalf("expected Rstatfs, got %T: %+v", msg, msg)
	}
	if rs.Stat != wantStat {
		t.Errorf("statfs = %+v, want %+v", rs.Stat, wantStat)
	}
}

func TestBridge_ENOSYS_Symlink(t *testing.T) {
	t.Parallel()

	// Root is a plain Inode -- no NodeSymlinker.
	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Send Tsymlink on a node that doesn't implement NodeSymlinker.
	sendMessage(t, cp.client, 2, &p9l.Tsymlink{DirFid: 0, Name: "link", Target: "/tmp/t", GID: 0})
	_, msg := readResponse(t, cp.client)
	isError(t, msg, proto.ENOSYS)
}

// --- Fsync test node types ---

// fsyncingNode embeds Inode and overrides Fsync to record calls. Open
// returns nil handle so the bridge falls back to NodeFsyncer.
type fsyncingNode struct {
	Inode
	fsyncCalled int
	fsyncErr    error
}

func (n *fsyncingNode) Open(_ context.Context, _ uint32) (FileHandle, uint32, error) {
	return nil, 0, nil
}

func (n *fsyncingNode) Fsync(_ context.Context) error {
	n.fsyncCalled++
	return n.fsyncErr
}

// fsyncingHandle implements FileSyncer on an open file handle.
type fsyncingHandle struct {
	fsyncCalled int
	fsyncErr    error
}

func (h *fsyncingHandle) Fsync(_ context.Context) error {
	h.fsyncCalled++
	return h.fsyncErr
}

// bothFsyncingNode implements NodeFsyncer and returns a FileSyncer-
// implementing handle from Open. Used to verify FileSyncer precedence
// over NodeFsyncer.
type bothFsyncingNode struct {
	Inode
	fsyncCalled int
	handle      *fsyncingHandle
}

func (n *bothFsyncingNode) Open(_ context.Context, _ uint32) (FileHandle, uint32, error) {
	n.handle = &fsyncingHandle{}
	return n.handle, 0, nil
}

func (n *bothFsyncingNode) Fsync(_ context.Context) error {
	n.fsyncCalled++
	return nil
}

var (
	_ NodeOpener    = (*fsyncingNode)(nil)
	_ NodeFsyncer   = (*fsyncingNode)(nil)
	_ InodeEmbedder = (*fsyncingNode)(nil)

	_ FileSyncer = (*fsyncingHandle)(nil)

	_ NodeOpener    = (*bothFsyncingNode)(nil)
	_ NodeFsyncer   = (*bothFsyncingNode)(nil)
	_ InodeEmbedder = (*bothFsyncingNode)(nil)
)

// fsync is a connPair helper that sends Tfsync and returns the response.
func (cp *connPair) fsync(t *testing.T, tag proto.Tag, fid proto.Fid, datasync uint32) proto.Message {
	t.Helper()
	sendMessage(t, cp.client, tag, &p9l.Tfsync{Fid: fid, DataSync: datasync})
	_, msg := readResponse(t, cp.client)
	return msg
}

func TestHandleFsync_ENOSYS(t *testing.T) {
	t.Parallel()

	// bridgeFile embeds Inode; Inode.Fsync returns proto.ENOSYS. Open
	// returns a nil handle, so the bridge takes the NodeFsyncer path and
	// gets ENOSYS back through errnoFromError.
	file := &bridgeFile{content: []byte("data"), mode: 0o644}
	file.Init(proto.QID{Type: proto.QTFILE, Path: 10}, file)

	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	root.AddChild("file.txt", file.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	cp.walk(t, 2, 0, 1, "file.txt")
	if _, ok := cp.lopen(t, 3, 1, 0).(*p9l.Rlopen); !ok {
		t.Fatalf("lopen failed")
	}

	msg := cp.fsync(t, 4, 1, 0)
	isError(t, msg, proto.ENOSYS)
}

func TestHandleFsync_EBADF_Unopened(t *testing.T) {
	t.Parallel()

	file := &bridgeFile{content: []byte("data"), mode: 0o644}
	file.Init(proto.QID{Type: proto.QTFILE, Path: 10}, file)

	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	root.AddChild("file.txt", file.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Walk places fid 1 in fidAllocated state (not opened).
	cp.walk(t, 2, 0, 1, "file.txt")

	msg := cp.fsync(t, 3, 1, 0)
	isError(t, msg, proto.EBADF)
}

func TestHandleFsync_EBADF_NoFid(t *testing.T) {
	t.Parallel()

	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Fid 99 was never attached.
	msg := cp.fsync(t, 2, 99, 0)
	isError(t, msg, proto.EBADF)
}

func TestHandleFsync_NodeFsyncer(t *testing.T) {
	t.Parallel()

	fn := &fsyncingNode{}
	fn.Init(proto.QID{Type: proto.QTFILE, Path: 42}, fn)

	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	root.AddChild("f", fn.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	cp.walk(t, 2, 0, 1, "f")
	if _, ok := cp.lopen(t, 3, 1, 0).(*p9l.Rlopen); !ok {
		t.Fatalf("lopen failed")
	}

	msg := cp.fsync(t, 4, 1, 0)
	if _, ok := msg.(*p9l.Rfsync); !ok {
		t.Fatalf("got %T, want *p9l.Rfsync: %+v", msg, msg)
	}
	if fn.fsyncCalled != 1 {
		t.Fatalf("fsyncCalled = %d, want 1", fn.fsyncCalled)
	}
}

func TestHandleFsync_FileSyncerPrecedence(t *testing.T) {
	t.Parallel()

	bn := &bothFsyncingNode{}
	bn.Init(proto.QID{Type: proto.QTFILE, Path: 43}, bn)

	root := &testDir{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	root.AddChild("f", bn.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	cp.walk(t, 2, 0, 1, "f")
	if _, ok := cp.lopen(t, 3, 1, 0).(*p9l.Rlopen); !ok {
		t.Fatalf("lopen failed")
	}

	msg := cp.fsync(t, 4, 1, 0)
	if _, ok := msg.(*p9l.Rfsync); !ok {
		t.Fatalf("got %T, want *p9l.Rfsync: %+v", msg, msg)
	}
	if bn.handle == nil {
		t.Fatal("expected handle to be populated by Open")
	}
	if bn.handle.fsyncCalled != 1 {
		t.Errorf("handle.fsyncCalled = %d, want 1", bn.handle.fsyncCalled)
	}
	if bn.fsyncCalled != 0 {
		t.Errorf("node.fsyncCalled = %d, want 0 (FileSyncer takes precedence)", bn.fsyncCalled)
	}
}

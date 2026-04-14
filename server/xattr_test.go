// Package server xattr_test.go contains the consolidated protocol-level test
// suite for xattr operations (xattrwalk, xattrcreate, read/write on the xattr
// fid, and clunk-to-commit). Shared mock node types (xattrFile, rawXattrFile,
// testXattrWriter) live in bridge_test.go because they are also referenced by
// limits_test.go (TestMaxFids_XattrwalkEMFILE).
package server

import (
	"bytes"
	"context"
	"testing"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// --- Xattr-specific test node types ---

// getterOnlyFile implements NodeXattrGetter but deliberately NOT
// NodeXattrSetter or NodeXattrRemover. It is used to verify that
// handleXattrcreate correctly rejects writes/removes on a node that
// advertises the read-only slice of the xattr surface (ENOSYS path at
// bridge.go:869 and :873).
type getterOnlyFile struct {
	Inode
	value []byte
}

func (f *getterOnlyFile) Open(_ context.Context, _ uint32) (FileHandle, uint32, error) {
	return nil, 0, nil
}

func (f *getterOnlyFile) GetXattr(_ context.Context, _ string) ([]byte, error) {
	return f.value, nil
}

// bothXattrFile implements BOTH RawXattrer AND the simple xattr interfaces
// (NodeXattrGetter/Setter/Lister/Remover). It exists to prove the bridge's
// priority-dispatch rule: when a node satisfies RawXattrer, the simple
// interfaces MUST NEVER be invoked. Every simple-interface method appends
// to simpleCalls so the test can assert it stayed empty.
type bothXattrFile struct {
	Inode
	xattrs        map[string][]byte
	lastWriteName string
	lastWriteData []byte
	simpleCalls   []string
}

func (f *bothXattrFile) Open(_ context.Context, _ uint32) (FileHandle, uint32, error) {
	return nil, 0, nil
}

// RawXattrer — returns distinctive data so the test can distinguish the path.
func (f *bothXattrFile) HandleXattrwalk(_ context.Context, _ string) ([]byte, error) {
	return []byte("from-raw"), nil
}

func (f *bothXattrFile) HandleXattrcreate(_ context.Context, name string, _ uint64, _ uint32) (XattrWriter, error) {
	return &bothXattrWriter{file: f, name: name}, nil
}

// Simple interfaces — record misrouting. If these are ever called when
// RawXattrer is present, TestXattr_Priority fails.
func (f *bothXattrFile) GetXattr(_ context.Context, name string) ([]byte, error) {
	f.simpleCalls = append(f.simpleCalls, "Get:"+name)
	return []byte("from-simple"), nil
}

func (f *bothXattrFile) SetXattr(_ context.Context, name string, _ []byte, _ uint32) error {
	f.simpleCalls = append(f.simpleCalls, "Set:"+name)
	return nil
}

func (f *bothXattrFile) ListXattrs(_ context.Context) ([]string, error) {
	f.simpleCalls = append(f.simpleCalls, "List")
	return []string{"from-simple"}, nil
}

func (f *bothXattrFile) RemoveXattr(_ context.Context, name string) error {
	f.simpleCalls = append(f.simpleCalls, "Remove:"+name)
	return nil
}

// bothXattrWriter is the XattrWriter returned by bothXattrFile.HandleXattrcreate.
// It mirrors testXattrWriter (bridge_test.go) but writes back into the outer
// bothXattrFile so the test can assert on lastWriteName/lastWriteData.
type bothXattrWriter struct {
	file *bothXattrFile
	name string
	data []byte
}

func (w *bothXattrWriter) Write(_ context.Context, data []byte) (int, error) {
	w.data = append(w.data, data...)
	return len(data), nil
}

func (w *bothXattrWriter) Commit(_ context.Context) error {
	if w.file.xattrs == nil {
		w.file.xattrs = make(map[string][]byte)
	}
	w.file.xattrs[w.name] = w.data
	w.file.lastWriteName = w.name
	w.file.lastWriteData = w.data
	return nil
}

// Compile-time capability assertions for the xattr-specific test mocks.
var (
	_ NodeOpener      = (*getterOnlyFile)(nil)
	_ NodeXattrGetter = (*getterOnlyFile)(nil)
	_ InodeEmbedder   = (*getterOnlyFile)(nil)

	_ NodeOpener       = (*bothXattrFile)(nil)
	_ RawXattrer       = (*bothXattrFile)(nil)
	_ NodeXattrGetter  = (*bothXattrFile)(nil)
	_ NodeXattrSetter  = (*bothXattrFile)(nil)
	_ NodeXattrLister  = (*bothXattrFile)(nil)
	_ NodeXattrRemover = (*bothXattrFile)(nil)
	_ InodeEmbedder    = (*bothXattrFile)(nil)

	_ XattrWriter = (*bothXattrWriter)(nil)
)

// --- Relocated tests (Task 1: from TestBridge_Xattr, TestBridge_RawXattr,
// TestBridge_XattrSizeMismatch). Logic is preserved verbatim -- the behaviour
// under test IS the regression surface. ---

// TestXattr_Get verifies simple NodeXattrGetter flow: Txattrwalk followed by
// Tread returns the attribute value; Rxattrwalk reports the correct size.
func TestXattr_Get(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}
	root := &symlinkDir{gen: gen}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)

	xf := &xattrFile{xattrs: map[string][]byte{"user.color": []byte("red")}}
	xf.Init(gen.Next(proto.QTFILE), xf)
	root.AddChild("xfile", xf.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Walk to xattr file.
	msg := cp.walk(t, 2, 0, 2, "xfile")
	if _, ok := msg.(*proto.Rwalk); !ok {
		t.Fatalf("expected Rwalk, got %T: %+v", msg, msg)
	}

	// Txattrwalk to get "user.color".
	sendMessage(t, cp.client, 10, &p9l.Txattrwalk{Fid: 2, NewFid: 10, Name: "user.color"})
	_, msg = readResponse(t, cp.client)
	rxw, ok := msg.(*p9l.Rxattrwalk)
	if !ok {
		t.Fatalf("expected Rxattrwalk, got %T: %+v", msg, msg)
	}
	if rxw.Size != 3 {
		t.Errorf("xattrwalk size = %d, want 3", rxw.Size)
	}

	// Read the xattr data.
	msg = cp.read(t, 11, 10, 0, 100)
	rr, ok := msg.(*proto.Rread)
	if !ok {
		t.Fatalf("expected Rread, got %T: %+v", msg, msg)
	}
	if string(rr.Data) != "red" {
		t.Errorf("xattr data = %q, want %q", string(rr.Data), "red")
	}

	// Clunk xattr fid.
	cp.clunk(t, 12, 10)
}

// TestXattr_List verifies NodeXattrLister flow: Txattrwalk with empty name
// returns a null-separated list of attribute names.
func TestXattr_List(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}
	root := &symlinkDir{gen: gen}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)

	xf := &xattrFile{xattrs: map[string][]byte{"user.color": []byte("red")}}
	xf.Init(gen.Next(proto.QTFILE), xf)
	root.AddChild("xfile", xf.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	msg := cp.walk(t, 2, 0, 2, "xfile")
	if _, ok := msg.(*proto.Rwalk); !ok {
		t.Fatalf("expected Rwalk, got %T: %+v", msg, msg)
	}

	// Txattrwalk with empty name to list.
	sendMessage(t, cp.client, 20, &p9l.Txattrwalk{Fid: 2, NewFid: 11, Name: ""})
	_, msg = readResponse(t, cp.client)
	rxw, ok := msg.(*p9l.Rxattrwalk)
	if !ok {
		t.Fatalf("expected Rxattrwalk, got %T: %+v", msg, msg)
	}
	if rxw.Size == 0 {
		t.Fatal("xattr list size should be > 0")
	}

	// Read list data.
	msg = cp.read(t, 21, 11, 0, 1024)
	rr, ok := msg.(*proto.Rread)
	if !ok {
		t.Fatalf("expected Rread, got %T: %+v", msg, msg)
	}
	// Should contain "user.color\0".
	if !bytes.Contains(rr.Data, []byte("user.color")) {
		t.Errorf("xattr list = %q, want to contain %q", string(rr.Data), "user.color")
	}

	cp.clunk(t, 22, 11)
}

// TestXattr_Set verifies the simple xattrcreate+write+clunk commit flow via
// NodeXattrSetter. The committed value is visible via a subsequent Txattrwalk.
func TestXattr_Set(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}
	root := &symlinkDir{gen: gen}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)

	xf := &xattrFile{xattrs: map[string][]byte{"user.color": []byte("red")}}
	xf.Init(gen.Next(proto.QTFILE), xf)
	root.AddChild("xfile", xf.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	msg := cp.walk(t, 2, 0, 2, "xfile")
	if _, ok := msg.(*proto.Rwalk); !ok {
		t.Fatalf("expected Rwalk, got %T: %+v", msg, msg)
	}

	// Clone fid 2 to fid 12 (walk with 0 names).
	msg = cp.walk(t, 30, 2, 12)
	if _, ok := msg.(*proto.Rwalk); !ok {
		t.Fatalf("expected Rwalk clone, got %T: %+v", msg, msg)
	}

	// Txattrcreate to set "user.size" with value "large" (5 bytes).
	sendMessage(t, cp.client, 31, &p9l.Txattrcreate{Fid: 12, Name: "user.size", AttrSize: 5, Flags: 0})
	_, msg = readResponse(t, cp.client)
	if _, ok := msg.(*p9l.Rxattrcreate); !ok {
		t.Fatalf("expected Rxattrcreate, got %T: %+v", msg, msg)
	}

	// Write the xattr data.
	msg = cp.write(t, 32, 12, 0, []byte("large"))
	if _, ok := msg.(*proto.Rwrite); !ok {
		t.Fatalf("expected Rwrite, got %T: %+v", msg, msg)
	}

	// Clunk to commit.
	msg = cp.clunk(t, 33, 12)
	if _, ok := msg.(*proto.Rclunk); !ok {
		t.Fatalf("expected Rclunk, got %T: %+v", msg, msg)
	}

	// Verify by reading the xattr back via xattrwalk.
	sendMessage(t, cp.client, 34, &p9l.Txattrwalk{Fid: 2, NewFid: 13, Name: "user.size"})
	_, msg = readResponse(t, cp.client)
	rxw, ok := msg.(*p9l.Rxattrwalk)
	if !ok {
		t.Fatalf("expected Rxattrwalk for verify, got %T: %+v", msg, msg)
	}
	if rxw.Size != 5 {
		t.Errorf("verify xattrwalk size = %d, want 5", rxw.Size)
	}

	msg = cp.read(t, 35, 13, 0, 100)
	rr, ok := msg.(*proto.Rread)
	if !ok {
		t.Fatalf("expected Rread for verify, got %T: %+v", msg, msg)
	}
	if string(rr.Data) != "large" {
		t.Errorf("verify xattr data = %q, want %q", string(rr.Data), "large")
	}

	cp.clunk(t, 36, 13)
}

// TestXattr_SizeMismatch verifies Pitfall 2 (RESEARCH.md): writing fewer bytes
// than Txattrcreate's declared AttrSize succeeds on each Twrite, but the
// mismatch surfaces as EIO on Tclunk (dispatch.go:232).
func TestXattr_SizeMismatch(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}
	root := &symlinkDir{gen: gen}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)

	xf := &xattrFile{xattrs: map[string][]byte{}}
	xf.Init(gen.Next(proto.QTFILE), xf)
	root.AddChild("xfile", xf.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Walk to xattr file.
	cp.walk(t, 2, 0, 2, "xfile")

	// Clone fid 2 to fid 3.
	cp.walk(t, 3, 2, 3)

	// Txattrcreate declaring size=3.
	sendMessage(t, cp.client, 4, &p9l.Txattrcreate{Fid: 3, Name: "test", AttrSize: 3, Flags: 0})
	_, msg := readResponse(t, cp.client)
	if _, ok := msg.(*p9l.Rxattrcreate); !ok {
		t.Fatalf("expected Rxattrcreate, got %T: %+v", msg, msg)
	}

	// Write only 2 bytes (declared 3).
	msg = cp.write(t, 5, 3, 0, []byte("ab"))
	if _, ok := msg.(*proto.Rwrite); !ok {
		t.Fatalf("expected Rwrite, got %T: %+v", msg, msg)
	}

	// Clunk should fail with EIO due to size mismatch.
	msg = cp.clunk(t, 6, 3)
	isError(t, msg, proto.EIO)
}

// TestXattr_Raw_Get verifies RawXattrer.HandleXattrwalk routes the read.
func TestXattr_Raw_Get(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}
	root := &symlinkDir{gen: gen}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)

	rxf := &rawXattrFile{xattrs: map[string][]byte{"raw.test": []byte("raw-value")}}
	rxf.Init(gen.Next(proto.QTFILE), rxf)
	root.AddChild("rawfile", rxf.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	msg := cp.walk(t, 2, 0, 2, "rawfile")
	if _, ok := msg.(*proto.Rwalk); !ok {
		t.Fatalf("expected Rwalk, got %T: %+v", msg, msg)
	}

	// Txattrwalk for "raw.test".
	sendMessage(t, cp.client, 10, &p9l.Txattrwalk{Fid: 2, NewFid: 20, Name: "raw.test"})
	_, msg = readResponse(t, cp.client)
	rxw, ok := msg.(*p9l.Rxattrwalk)
	if !ok {
		t.Fatalf("expected Rxattrwalk, got %T: %+v", msg, msg)
	}
	if rxw.Size != 9 {
		t.Errorf("xattrwalk size = %d, want 9", rxw.Size)
	}

	// Read the xattr data.
	msg = cp.read(t, 11, 20, 0, 100)
	rr, ok := msg.(*proto.Rread)
	if !ok {
		t.Fatalf("expected Rread, got %T: %+v", msg, msg)
	}
	if string(rr.Data) != "raw-value" {
		t.Errorf("xattr data = %q, want %q", string(rr.Data), "raw-value")
	}

	cp.clunk(t, 12, 20)
}

// TestXattr_Raw_Set verifies RawXattrer.HandleXattrcreate + XattrWriter.Commit
// receive the complete write payload on Tclunk. Unlike the simple-interface
// path, RawXattrer bypasses the bridge's size-mismatch check (Pitfall 3).
func TestXattr_Raw_Set(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}
	root := &symlinkDir{gen: gen}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)

	rxf := &rawXattrFile{xattrs: map[string][]byte{"raw.test": []byte("raw-value")}}
	rxf.Init(gen.Next(proto.QTFILE), rxf)
	root.AddChild("rawfile", rxf.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	msg := cp.walk(t, 2, 0, 2, "rawfile")
	if _, ok := msg.(*proto.Rwalk); !ok {
		t.Fatalf("expected Rwalk, got %T: %+v", msg, msg)
	}

	// Clone fid 2 to fid 21.
	msg = cp.walk(t, 20, 2, 21)
	if _, ok := msg.(*proto.Rwalk); !ok {
		t.Fatalf("expected Rwalk clone, got %T: %+v", msg, msg)
	}

	// Txattrcreate.
	sendMessage(t, cp.client, 21, &p9l.Txattrcreate{Fid: 21, Name: "raw.new", AttrSize: 7, Flags: 0})
	_, msg = readResponse(t, cp.client)
	if _, ok := msg.(*p9l.Rxattrcreate); !ok {
		t.Fatalf("expected Rxattrcreate, got %T: %+v", msg, msg)
	}

	// Write data.
	msg = cp.write(t, 22, 21, 0, []byte("written"))
	if _, ok := msg.(*proto.Rwrite); !ok {
		t.Fatalf("expected Rwrite, got %T: %+v", msg, msg)
	}

	// Clunk to commit via XattrWriter.
	msg = cp.clunk(t, 23, 21)
	if _, ok := msg.(*proto.Rclunk); !ok {
		t.Fatalf("expected Rclunk, got %T: %+v", msg, msg)
	}

	// Verify the raw xattr file received the write.
	if rxf.lastWriteName != "raw.new" {
		t.Errorf("lastWriteName = %q, want %q", rxf.lastWriteName, "raw.new")
	}
	if string(rxf.xattrs["raw.new"]) != "written" {
		t.Errorf("xattrs[raw.new] = %q, want %q", string(rxf.xattrs["raw.new"]), "written")
	}
}


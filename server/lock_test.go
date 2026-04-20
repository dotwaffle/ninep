package server

import (
	"context"
	"net"
	"sync"
	"testing"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// openableFile implements NodeOpener but NOT NodeLocker. Used to exercise the
// ENOSYS dispatch path in handleLock/handleGetlock: the fid reaches fidOpened
// state (because Open succeeds), but the node type-assertion to NodeLocker
// fails and the bridge must return Rlerror{ENOSYS}.
type openableFile struct {
	Inode
}

func (f *openableFile) Open(_ context.Context, _ uint32) (FileHandle, uint32, error) {
	return nil, 0, nil
}

var (
	_ NodeOpener    = (*openableFile)(nil)
	_ InodeEmbedder = (*openableFile)(nil)
)

// contentionLockFile tracks a single held POSIX record lock keyed by ClientID.
// Lock returns Blocked when another client holds a conflicting lock; GetLock
// returns the holder's parameters when there is a conflict. Internal state is
// sync.Mutex-guarded per Pitfall 4 -- the bridge does not serialize NodeLocker
// calls (per-connection handler goroutines race on shared Node state), so the
// mock must.
//
// Read-lock sharing: two RdLck from different clients both return OK (matching
// POSIX F_RDLCK semantics); the held metadata reflects the first acquiring
// client. A WrLck conflicts with any existing holder from another client.
type contentionLockFile struct {
	Inode
	mu         sync.Mutex
	heldBy     string // "" means no holder.
	heldType   proto.LockType
	heldStart  uint64
	heldLength uint64
	heldProcID uint32
}

func (f *contentionLockFile) Open(_ context.Context, _ uint32) (FileHandle, uint32, error) {
	return nil, 0, nil
}

func (f *contentionLockFile) Lock(
	_ context.Context, lockType proto.LockType, _ proto.LockFlags,
	start, length uint64, procID uint32, clientID string,
) (proto.LockStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if lockType == proto.LockTypeUnlck {
		if f.heldBy == clientID {
			f.heldBy = ""
		}
		return proto.LockStatusOK, nil
	}

	// Read-lock compatibility: readers may coexist.
	if lockType == proto.LockTypeRdLck && f.heldBy != "" && f.heldType == proto.LockTypeRdLck {
		return proto.LockStatusOK, nil
	}

	if f.heldBy != "" && f.heldBy != clientID {
		return proto.LockStatusBlocked, nil
	}

	f.heldBy = clientID
	f.heldType = lockType
	f.heldStart = start
	f.heldLength = length
	f.heldProcID = procID
	return proto.LockStatusOK, nil
}

func (f *contentionLockFile) GetLock(
	_ context.Context, _ proto.LockType,
	start, length uint64, procID uint32, clientID string,
) (proto.LockType, uint64, uint64, uint32, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.heldBy == "" || f.heldBy == clientID {
		return proto.LockTypeUnlck, start, length, procID, clientID, nil
	}
	return f.heldType, f.heldStart, f.heldLength, f.heldProcID, f.heldBy, nil
}

var (
	_ NodeOpener    = (*contentionLockFile)(nil)
	_ NodeLocker    = (*contentionLockFile)(nil)
	_ InodeEmbedder = (*contentionLockFile)(nil)
)

// twoConnPair builds ONE server.Server serving root and returns two
// independent client connections, each with its OWN cancellable context,
// its OWN cleanup (cancel -> close pipes -> wait on done), and version
// negotiation completed before returning.
//
// Callers must NOT call cp.close(t) on the returned pairs -- lifecycle is
// fully owned by tb.Cleanup. Each connection's ServeConn goroutine is
// independently cancellable so there are no races on shared close.
//
// Node state (locks, xattrs, etc.) is the intentional shared surface across
// the two connections; fid tables are per-connection.
func twoConnPair(tb testing.TB, root Node, opts ...Option) (*connPair, *connPair) {
	tb.Helper()

	defaultOpts := []Option{WithMaxMsize(65536), WithLogger(discardLogger())}
	srv := New(root, append(defaultOpts, opts...)...)

	mkPair := func() *connPair {
		ctx, cancel := context.WithCancel(tb.Context())
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			srv.ServeConn(ctx, server)
		}()

		sendTversion(tb, client, 65536, "9P2000.L")
		rv := readRversion(tb, client)
		if rv.Version != "9P2000.L" {
			tb.Fatalf("version negotiation failed: got %q", rv.Version)
		}

		tb.Cleanup(func() {
			cancel()
			_ = client.Close()
			_ = server.Close()
			<-done
		})

		return &connPair{client: client, done: done, cancel: cancel}
	}

	return mkPair(), mkPair()
}

// --- Single-connection lock tests (relocated from bridge_test.go) ---

// TestLock_SingleConn exercises the single-connection Tlock happy path.
// Relocated verbatim from TestBridge_Lock in bridge_test.go.
func TestLock_SingleConn(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}
	root := &symlinkDir{gen: gen}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)

	lf := &lockableFile{lockStatus: proto.LockStatusOK}
	lf.Init(gen.Next(proto.QTFILE), lf)
	root.AddChild("lockfile", lf.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Walk to file, open it.
	cp.walk(t, 2, 0, 2, "lockfile")
	msg := cp.lopen(t, 3, 2, 0)
	if _, ok := msg.(*p9l.Rlopen); !ok {
		t.Fatalf("expected Rlopen, got %T: %+v", msg, msg)
	}

	// Send Tlock.
	sendMessage(t, cp.client, 4, &p9l.Tlock{
		Fid:      2,
		LockType: proto.LockTypeWrLck,
		Flags:    0,
		Start:    0,
		Length:   100,
		ProcID:   1234,
		ClientID: "test",
	})
	_, msg = readResponse(t, cp.client)
	rl, ok := msg.(*p9l.Rlock)
	if !ok {
		t.Fatalf("expected Rlock, got %T: %+v", msg, msg)
	}
	if rl.Status != proto.LockStatusOK {
		t.Errorf("lock status = %d, want LockStatusOK (%d)", rl.Status, proto.LockStatusOK)
	}
}

// TestLock_Getlock exercises the single-connection Tgetlock happy path.
// Relocated verbatim from TestBridge_Getlock in bridge_test.go.
func TestLock_Getlock(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}
	root := &symlinkDir{gen: gen}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)

	lf := &lockableFile{lockStatus: proto.LockStatusOK}
	lf.Init(gen.Next(proto.QTFILE), lf)
	root.AddChild("lockfile", lf.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Walk to file, open it.
	cp.walk(t, 2, 0, 2, "lockfile")
	msg := cp.lopen(t, 3, 2, 0)
	if _, ok := msg.(*p9l.Rlopen); !ok {
		t.Fatalf("expected Rlopen, got %T: %+v", msg, msg)
	}

	// Send Tgetlock.
	sendMessage(t, cp.client, 4, &p9l.Tgetlock{
		Fid:      2,
		LockType: proto.LockTypeRdLck,
		Start:    0,
		Length:   100,
		ProcID:   1234,
		ClientID: "test",
	})
	_, msg = readResponse(t, cp.client)
	rgl, ok := msg.(*p9l.Rgetlock)
	if !ok {
		t.Fatalf("expected Rgetlock, got %T: %+v", msg, msg)
	}
	if rgl.LockType != proto.LockTypeRdLck {
		t.Errorf("getlock type = %d, want LockTypeRdLck (%d)", rgl.LockType, proto.LockTypeRdLck)
	}
	if rgl.Start != 0 || rgl.Length != 100 {
		t.Errorf("getlock range = [%d, %d), want [0, 100)", rgl.Start, rgl.Length)
	}
	if rgl.ProcID != 1234 {
		t.Errorf("getlock procID = %d, want 1234", rgl.ProcID)
	}
	if rgl.ClientID != "test" {
		t.Errorf("getlock clientID = %q, want %q", rgl.ClientID, "test")
	}
}

// TestLock_EBADF_Unopened verifies that Tlock on a fid that has been walked
// but not opened returns EBADF (bridge.go: fid must be fidOpened).
func TestLock_EBADF_Unopened(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}
	root := &symlinkDir{gen: gen}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)

	lf := &lockableFile{lockStatus: proto.LockStatusOK}
	lf.Init(gen.Next(proto.QTFILE), lf)
	root.AddChild("lockfile", lf.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Walk but do NOT open -- fid 2 stays in fidAllocated state.
	cp.walk(t, 2, 0, 2, "lockfile")

	sendMessage(t, cp.client, 3, &p9l.Tlock{
		Fid:      2,
		LockType: proto.LockTypeWrLck,
		Flags:    0,
		Start:    0,
		Length:   100,
		ProcID:   1,
		ClientID: "test",
	})
	_, msg := readResponse(t, cp.client)
	isError(t, msg, proto.EBADF)
}

// TestLock_Getlock_EBADF_Unopened verifies that Tgetlock on a fid that has
// been walked but not opened returns EBADF.
func TestLock_Getlock_EBADF_Unopened(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}
	root := &symlinkDir{gen: gen}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)

	lf := &lockableFile{lockStatus: proto.LockStatusOK}
	lf.Init(gen.Next(proto.QTFILE), lf)
	root.AddChild("lockfile", lf.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	// Walk but do NOT open.
	cp.walk(t, 2, 0, 2, "lockfile")

	sendMessage(t, cp.client, 3, &p9l.Tgetlock{
		Fid:      2,
		LockType: proto.LockTypeWrLck,
		Start:    0,
		Length:   100,
		ProcID:   1,
		ClientID: "test",
	})
	_, msg := readResponse(t, cp.client)
	isError(t, msg, proto.EBADF)
}

// TestLock_ENOSYS verifies that Tlock on an opened fid whose node does NOT
// implement NodeLocker returns ENOSYS.
func TestLock_ENOSYS(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}
	root := &symlinkDir{gen: gen}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)

	of := &openableFile{}
	of.Init(gen.Next(proto.QTFILE), of)
	root.AddChild("plain", of.EmbeddedInode())

	cp := setupBridgeConn(t, root)
	defer cp.close(t)

	cp.walk(t, 2, 0, 2, "plain")
	msg := cp.lopen(t, 3, 2, 0)
	if _, ok := msg.(*p9l.Rlopen); !ok {
		t.Fatalf("expected Rlopen, got %T: %+v", msg, msg)
	}

	sendMessage(t, cp.client, 4, &p9l.Tlock{
		Fid:      2,
		LockType: proto.LockTypeWrLck,
		Flags:    0,
		Start:    0,
		Length:   100,
		ProcID:   1,
		ClientID: "test",
	})
	_, msg = readResponse(t, cp.client)
	isError(t, msg, proto.ENOSYS)
}

// --- twoConnPair smoke test ---

// TestTwoConnPair_SmokeTest verifies the twoConnPair helper wires both
// clients to a shared Server and both can complete attach.
func TestTwoConnPair_SmokeTest(t *testing.T) {
	t.Parallel()

	gen := &QIDGenerator{}
	root := &symlinkDir{gen: gen}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)

	a, b := twoConnPair(t, root)
	a.attach(t, 1, 0, "A", "")
	b.attach(t, 1, 0, "B", "")
}

// --- Multi-connection contention tests (TEST-02) ---

// contentionSetup creates a symlinkDir root with a contentionLockFile named
// "lockfile" and returns (a, b, lf) where a and b are two connections to the
// same server sharing lf's state.
func contentionSetup(t *testing.T) (*connPair, *connPair, *contentionLockFile) {
	t.Helper()

	gen := &QIDGenerator{}
	root := &symlinkDir{gen: gen}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)

	lf := &contentionLockFile{}
	lf.Init(gen.Next(proto.QTFILE), lf)
	root.AddChild("lockfile", lf.EmbeddedInode())

	a, b := twoConnPair(t, root)
	return a, b, lf
}

// contentionAttachOpen attaches the given connection, walks to "lockfile" on
// fid 1, and opens fid 1. The client identifies itself via the uname argument.
func contentionAttachOpen(t *testing.T, cp *connPair, uname string) {
	t.Helper()
	cp.attach(t, 1, 0, uname, "")
	msg := cp.walk(t, 2, 0, 1, "lockfile")
	if _, ok := msg.(*proto.Rwalk); !ok {
		t.Fatalf("%s walk: expected Rwalk, got %T: %+v", uname, msg, msg)
	}
	openMsg := cp.lopen(t, 3, 1, 0)
	if _, ok := openMsg.(*p9l.Rlopen); !ok {
		t.Fatalf("%s open: expected Rlopen, got %T: %+v", uname, openMsg, openMsg)
	}
}

// TestLock_Contention_WriteBlocksWrite: conn A acquires WrLck, conn B's
// Tgetlock sees A as holder, conn B's Tlock (both non-blocking and
// LockFlagBlock variants) returns Blocked.
func TestLock_Contention_WriteBlocksWrite(t *testing.T) {
	t.Parallel()

	a, b, _ := contentionSetup(t)
	contentionAttachOpen(t, a, "A")
	contentionAttachOpen(t, b, "B")

	// A acquires WrLck.
	sendMessage(t, a.client, 4, &p9l.Tlock{
		Fid: 1, LockType: proto.LockTypeWrLck, Flags: 0,
		Start: 0, Length: 100, ProcID: 1, ClientID: "A",
	})
	_, msg := readResponse(t, a.client)
	rl, ok := msg.(*p9l.Rlock)
	if !ok {
		t.Fatalf("A lock: expected Rlock, got %T: %+v", msg, msg)
	}
	if rl.Status != proto.LockStatusOK {
		t.Fatalf("A lock: status = %v, want LockStatusOK", rl.Status)
	}

	// B's Tgetlock sees A as holder.
	sendMessage(t, b.client, 4, &p9l.Tgetlock{
		Fid: 1, LockType: proto.LockTypeWrLck,
		Start: 0, Length: 100, ProcID: 2, ClientID: "B",
	})
	_, msg = readResponse(t, b.client)
	rgl, ok := msg.(*p9l.Rgetlock)
	if !ok {
		t.Fatalf("B getlock: expected Rgetlock, got %T: %+v", msg, msg)
	}
	if rgl.LockType != proto.LockTypeWrLck {
		t.Errorf("B getlock type = %v, want WrLck (conflict)", rgl.LockType)
	}
	if rgl.ClientID != "A" {
		t.Errorf("B getlock clientID = %q, want A", rgl.ClientID)
	}
	if rgl.ProcID != 1 {
		t.Errorf("B getlock procID = %d, want 1", rgl.ProcID)
	}

	// B's Tlock (non-blocking) returns Blocked.
	sendMessage(t, b.client, 5, &p9l.Tlock{
		Fid: 1, LockType: proto.LockTypeWrLck, Flags: 0,
		Start: 0, Length: 100, ProcID: 2, ClientID: "B",
	})
	_, msg = readResponse(t, b.client)
	rl, ok = msg.(*p9l.Rlock)
	if !ok {
		t.Fatalf("B lock (non-blocking): expected Rlock, got %T: %+v", msg, msg)
	}
	if rl.Status != proto.LockStatusBlocked {
		t.Errorf("B lock (non-blocking): status = %v, want Blocked", rl.Status)
	}

	// B's Tlock with LockFlagBlock still returns Blocked (mock ignores
	// blocking policy; bridge passes Flags through unchanged).
	sendMessage(t, b.client, 6, &p9l.Tlock{
		Fid: 1, LockType: proto.LockTypeWrLck, Flags: proto.LockFlagBlock,
		Start: 0, Length: 100, ProcID: 2, ClientID: "B",
	})
	_, msg = readResponse(t, b.client)
	rl, ok = msg.(*p9l.Rlock)
	if !ok {
		t.Fatalf("B lock (LockFlagBlock): expected Rlock, got %T: %+v", msg, msg)
	}
	if rl.Status != proto.LockStatusBlocked {
		t.Errorf("B lock (LockFlagBlock): status = %v, want Blocked", rl.Status)
	}
}

// TestLock_Contention_ReadReadOK: two read locks from different clients
// coexist. The contentionLockFile models POSIX F_RDLCK sharing semantics.
func TestLock_Contention_ReadReadOK(t *testing.T) {
	t.Parallel()

	a, b, _ := contentionSetup(t)
	contentionAttachOpen(t, a, "A")
	contentionAttachOpen(t, b, "B")

	// A acquires RdLck.
	sendMessage(t, a.client, 4, &p9l.Tlock{
		Fid: 1, LockType: proto.LockTypeRdLck, Flags: 0,
		Start: 0, Length: 100, ProcID: 1, ClientID: "A",
	})
	_, msg := readResponse(t, a.client)
	if rl, ok := msg.(*p9l.Rlock); !ok {
		t.Fatalf("A rdlck: expected Rlock, got %T: %+v", msg, msg)
	} else if rl.Status != proto.LockStatusOK {
		t.Fatalf("A rdlck: status = %v, want OK", rl.Status)
	}

	// B also acquires RdLck -- readers coexist.
	sendMessage(t, b.client, 4, &p9l.Tlock{
		Fid: 1, LockType: proto.LockTypeRdLck, Flags: 0,
		Start: 0, Length: 100, ProcID: 2, ClientID: "B",
	})
	_, msg = readResponse(t, b.client)
	if rl, ok := msg.(*p9l.Rlock); !ok {
		t.Fatalf("B rdlck: expected Rlock, got %T: %+v", msg, msg)
	} else if rl.Status != proto.LockStatusOK {
		t.Errorf("B rdlck: status = %v, want OK (read locks should share)", rl.Status)
	}
}

// TestLock_Contention_UnlckReleases: A acquires WrLck -> OK, B's WrLck is
// Blocked, A sends Unlck -> OK, B's WrLck now succeeds.
func TestLock_Contention_UnlckReleases(t *testing.T) {
	t.Parallel()

	a, b, _ := contentionSetup(t)
	contentionAttachOpen(t, a, "A")
	contentionAttachOpen(t, b, "B")

	// A WrLck -> OK.
	sendMessage(t, a.client, 4, &p9l.Tlock{
		Fid: 1, LockType: proto.LockTypeWrLck, Flags: 0,
		Start: 0, Length: 100, ProcID: 1, ClientID: "A",
	})
	_, msg := readResponse(t, a.client)
	if rl, ok := msg.(*p9l.Rlock); !ok || rl.Status != proto.LockStatusOK {
		t.Fatalf("A wrlck: want Rlock{OK}, got %T: %+v", msg, msg)
	}

	// B WrLck -> Blocked.
	sendMessage(t, b.client, 4, &p9l.Tlock{
		Fid: 1, LockType: proto.LockTypeWrLck, Flags: 0,
		Start: 0, Length: 100, ProcID: 2, ClientID: "B",
	})
	_, msg = readResponse(t, b.client)
	if rl, ok := msg.(*p9l.Rlock); !ok || rl.Status != proto.LockStatusBlocked {
		t.Fatalf("B wrlck (before unlck): want Rlock{Blocked}, got %T: %+v", msg, msg)
	}

	// A Unlck -> OK.
	sendMessage(t, a.client, 5, &p9l.Tlock{
		Fid: 1, LockType: proto.LockTypeUnlck, Flags: 0,
		Start: 0, Length: 100, ProcID: 1, ClientID: "A",
	})
	_, msg = readResponse(t, a.client)
	if rl, ok := msg.(*p9l.Rlock); !ok || rl.Status != proto.LockStatusOK {
		t.Fatalf("A unlck: want Rlock{OK}, got %T: %+v", msg, msg)
	}

	// B WrLck -> OK.
	sendMessage(t, b.client, 5, &p9l.Tlock{
		Fid: 1, LockType: proto.LockTypeWrLck, Flags: 0,
		Start: 0, Length: 100, ProcID: 2, ClientID: "B",
	})
	_, msg = readResponse(t, b.client)
	if rl, ok := msg.(*p9l.Rlock); !ok || rl.Status != proto.LockStatusOK {
		t.Fatalf("B wrlck (after A unlck): want Rlock{OK}, got %T: %+v", msg, msg)
	}
}

// TestLock_Concurrent stresses contentionLockFile under -race. G=4 connections
// each run a goroutine that cycles Lock/Unlck K=25 times. No data races, no
// deadlocks, no panics. Per 09-03 precedent, each goroutine owns its own
// connection (net.Pipe serializes Read/Write pairs).
func TestLock_Concurrent(t *testing.T) {
	t.Parallel()

	const (
		numConns  = 4
		numCycles = 25
	)

	gen := &QIDGenerator{}
	root := &symlinkDir{gen: gen}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)

	lf := &contentionLockFile{}
	lf.Init(gen.Next(proto.QTFILE), lf)
	root.AddChild("lockfile", lf.EmbeddedInode())

	// Build G connections using twoConnPair (which produces 2 at a time).
	conns := make([]*connPair, 0, numConns)
	for range numConns / 2 {
		a, b := twoConnPair(t, root)
		conns = append(conns, a, b)
	}

	// Each connection attaches + walks + opens.
	for i, cp := range conns {
		uname := string(rune('A' + i))
		cp.attach(t, 1, 0, uname, "")
		msg := cp.walk(t, 2, 0, 1, "lockfile")
		if _, ok := msg.(*proto.Rwalk); !ok {
			t.Fatalf("conn %d walk: expected Rwalk, got %T: %+v", i, msg, msg)
		}
		openMsg := cp.lopen(t, 3, 1, 0)
		if _, ok := openMsg.(*p9l.Rlopen); !ok {
			t.Fatalf("conn %d open: expected Rlopen, got %T: %+v", i, openMsg, openMsg)
		}
	}

	// Each connection runs Lock/Unlck cycles on its own goroutine.
	var wg sync.WaitGroup
	wg.Add(len(conns))
	for i, cp := range conns {
		go func() {
			defer wg.Done()
			clientID := string(rune('A' + i))
			// Unique tag range per connection to avoid tag collisions.
			baseTag := proto.Tag(100 + i*100)
			for k := range numCycles {
				tag := baseTag + proto.Tag(k*2)
				// Try to acquire WrLck (may succeed or be Blocked).
				sendMessage(t, cp.client, tag, &p9l.Tlock{
					Fid: 1, LockType: proto.LockTypeWrLck, Flags: 0,
					Start: 0, Length: 100, ProcID: uint32(i + 1), ClientID: clientID,
				})
				_, msg := readResponse(t, cp.client)
				rl, ok := msg.(*p9l.Rlock)
				if !ok {
					t.Errorf("conn %d cycle %d lock: expected Rlock, got %T: %+v", i, k, msg, msg)
					return
				}
				if rl.Status != proto.LockStatusOK && rl.Status != proto.LockStatusBlocked {
					t.Errorf("conn %d cycle %d lock: unexpected status %v", i, k, rl.Status)
					return
				}

				// If we got it, release it.
				if rl.Status == proto.LockStatusOK {
					sendMessage(t, cp.client, tag+1, &p9l.Tlock{
						Fid: 1, LockType: proto.LockTypeUnlck, Flags: 0,
						Start: 0, Length: 100, ProcID: uint32(i + 1), ClientID: clientID,
					})
					_, msg := readResponse(t, cp.client)
					if _, ok := msg.(*p9l.Rlock); !ok {
						t.Errorf("conn %d cycle %d unlck: expected Rlock, got %T: %+v", i, k, msg, msg)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
}

package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// TestMaxConnections_RejectsExcess verifies that ServeConn rejects the
// (N+1)th connection immediately when WithMaxConnections(N) is configured.
// The rejected connection must be closed before ServeConn returns.
func TestMaxConnections_RejectsExcess(t *testing.T) {
	t.Parallel()
	root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})
	srv := New(root, WithMaxConnections(1), WithLogger(discardLogger()))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)

	// First connection: accepted; negotiate Tversion to ensure it is serving.
	c1Client, c1Server := net.Pipe()
	t.Cleanup(func() { _ = c1Client.Close() })
	t.Cleanup(func() { _ = c1Server.Close() }) // belt-and-braces
	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		srv.ServeConn(ctx, c1Server)
	}()
	sendTversion(t, c1Client, 65536, "9P2000.L")
	_ = readRversion(t, c1Client)

	// Second connection: must be rejected — ServeConn must return fast.
	c2Client, c2Server := net.Pipe()
	t.Cleanup(func() { _ = c2Client.Close() })
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		srv.ServeConn(ctx, c2Server)
	}()

	select {
	case <-done2:
		// ok — ServeConn returned immediately
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("ServeConn did not return on rejected connection within 500ms")
	}

	// The rejected conn should be closed — read returns error.
	buf := make([]byte, 1)
	_ = c2Client.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, err := c2Client.Read(buf); err == nil {
		t.Fatalf("expected read error on rejected conn, got nil")
	}

	// Clean up c1 — closing client lets the first ServeConn drain.
	_ = c1Client.Close()
	<-done1

	if got := srv.connCount.Load(); got != 0 {
		t.Fatalf("connCount = %d, want 0 after cleanup", got)
	}
}

// TestMaxConnections_ZeroUnlimited verifies that leaving WithMaxConnections
// unset (or passing 0) imposes no limit.
func TestMaxConnections_ZeroUnlimited(t *testing.T) {
	t.Parallel()
	root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})
	srv := New(root, WithLogger(discardLogger()))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)

	for range 3 {
		cc, sc := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			srv.ServeConn(ctx, sc)
		}()
		sendTversion(t, cc, 65536, "9P2000.L")
		_ = readRversion(t, cc)
		_ = cc.Close()
		<-done
	}

	if got := srv.connCount.Load(); got != 0 {
		t.Fatalf("connCount = %d, want 0 (unlimited mode should not touch counter)", got)
	}
}

// TestMaxConnections_NoCounterLeak verifies that after many sequential
// connections, connCount returns to 0 (defer Add(-1) runs on every exit path).
func TestMaxConnections_NoCounterLeak(t *testing.T) {
	t.Parallel()
	root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})
	srv := New(root, WithMaxConnections(2), WithLogger(discardLogger()))

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)

	for range 20 {
		cc, sc := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			srv.ServeConn(ctx, sc)
		}()
		sendTversion(t, cc, 65536, "9P2000.L")
		_ = readRversion(t, cc)
		_ = cc.Close()
		<-done
	}

	if got := srv.connCount.Load(); got != 0 {
		t.Fatalf("connCount = %d, want 0 after all connections closed", got)
	}
}

// TestMaxConnections_ConcurrentAccept launches 2N goroutines each calling
// ServeConn concurrently. Exactly N should successfully negotiate Tversion;
// the other N should be rejected. After all exit, connCount must be 0.
func TestMaxConnections_ConcurrentAccept(t *testing.T) {
	t.Parallel()
	const N = 8
	root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})
	srv := New(root, WithMaxConnections(N), WithLogger(discardLogger()))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)

	var wg sync.WaitGroup
	var accepted, rejected atomic.Int64
	clients := make([]net.Conn, 2*N)
	servers := make([]net.Conn, 2*N)
	for i := range 2 * N {
		cc, sc := net.Pipe()
		clients[i] = cc
		servers[i] = sc
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv.ServeConn(ctx, sc)
		}()
	}

	var negWg sync.WaitGroup
	for _, cc := range clients {
		negWg.Add(1)
		go func(c net.Conn) {
			defer negWg.Done()
			_ = c.SetDeadline(time.Now().Add(2 * time.Second))
			if err := writeTversionRaw(c, 65536, "9P2000.L"); err != nil {
				rejected.Add(1)
				return
			}
			if _, err := readRversionOrErr(c); err != nil {
				rejected.Add(1)
				return
			}
			accepted.Add(1)
		}(cc)
	}
	negWg.Wait()

	// Close all clients to let servers drain.
	for _, cc := range clients {
		_ = cc.Close()
	}
	wg.Wait()

	if got := accepted.Load(); got != N {
		t.Fatalf("accepted = %d, want %d", got, N)
	}
	if got := rejected.Load(); got != N {
		t.Fatalf("rejected = %d, want %d", got, N)
	}
	if got := srv.connCount.Load(); got != 0 {
		t.Fatalf("connCount = %d, want 0", got)
	}
}

// writeTversionRaw is an err-returning variant of sendTversion for tests that
// need to observe write failures (e.g. when the server rejected and closed
// the conn before we wrote).
func writeTversionRaw(w net.Conn, msize uint32, version string) error {
	var body bytes.Buffer
	tv := &proto.Tversion{Msize: msize, Version: version}
	if err := tv.EncodeTo(&body); err != nil {
		return err
	}
	size := uint32(proto.HeaderSize) + uint32(body.Len())
	if err := proto.WriteUint32(w, size); err != nil {
		return err
	}
	if err := proto.WriteUint8(w, uint8(proto.TypeTversion)); err != nil {
		return err
	}
	if err := proto.WriteUint16(w, uint16(proto.NoTag)); err != nil {
		return err
	}
	_, err := w.Write(body.Bytes())
	return err
}

// readRversionOrErr is an err-returning variant of readRversion.
func readRversionOrErr(r net.Conn) (*proto.Rversion, error) {
	size, err := proto.ReadUint32(r)
	if err != nil {
		return nil, err
	}
	if _, err := proto.ReadUint8(r); err != nil { // type
		return nil, err
	}
	if _, err := proto.ReadUint16(r); err != nil { // tag
		return nil, err
	}
	bodySize := int64(size) - int64(proto.HeaderSize)
	var rv proto.Rversion
	if err := rv.DecodeFrom(io.LimitReader(r, bodySize)); err != nil {
		return nil, err
	}
	return &rv, nil
}

// sendAttachExpectError sends a Tattach and returns the raw response. Unlike
// connPair.attach it does not fail on non-Rattach responses -- the caller
// inspects the returned message (typically expecting Rlerror{EMFILE}).
func sendAttachExpectError(t *testing.T, cp *connPair, tag proto.Tag, fid proto.Fid) proto.Message {
	t.Helper()
	sendMessage(t, cp.client, tag, &proto.Tattach{
		Fid:   fid,
		Afid:  proto.NoFid,
		Uname: "u",
		Aname: "",
	})
	_, msg := readResponse(t, cp.client)
	return msg
}

// TestMaxFids_ZeroUnlimited verifies that WithMaxFids(0) imposes no limit:
// many sequential fid-creating operations all succeed.
func TestMaxFids_ZeroUnlimited(t *testing.T) {
	t.Parallel()
	root := testTree()
	cp := newConnPair(t, root, WithMaxFids(0))
	t.Cleanup(func() { cp.close(t) })

	// Attach fid 0, then clone to fids 1..9. All must succeed.
	cp.attach(t, 1, 0, "u", "")
	for i := proto.Fid(1); i <= 9; i++ {
		resp := cp.walk(t, proto.Tag(10+i), 0, i)
		if _, ok := resp.(*proto.Rwalk); !ok {
			t.Fatalf("clone to fid %d: expected Rwalk, got %T: %+v", i, resp, resp)
		}
	}
}

// TestMaxFids_AttachReturnsEMFILE verifies that Tattach at the fid cap
// returns Rlerror{EMFILE} rather than silently succeeding.
func TestMaxFids_AttachReturnsEMFILE(t *testing.T) {
	t.Parallel()
	root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})
	cp := newConnPair(t, root, WithMaxFids(1))
	t.Cleanup(func() { cp.close(t) })

	cp.attach(t, 1, 0, "u", "")               // 1/1
	msg := sendAttachExpectError(t, cp, 2, 1) // 2/1 -> EMFILE
	isError(t, msg, proto.EMFILE)
}

// TestMaxFids_WalkCloneReturnsEMFILE verifies that a Twalk clone
// (nwname=0) at the fid cap returns Rlerror{EMFILE}.
func TestMaxFids_WalkCloneReturnsEMFILE(t *testing.T) {
	t.Parallel()
	root := testTree()
	cp := newConnPair(t, root, WithMaxFids(2))
	t.Cleanup(func() { cp.close(t) })

	cp.attach(t, 1, 0, "u", "") // 1/2
	resp := cp.walk(t, 2, 0, 1) // clone -> 2/2
	if _, ok := resp.(*proto.Rwalk); !ok {
		t.Fatalf("expected Rwalk for clone, got %T: %+v", resp, resp)
	}
	resp = cp.walk(t, 3, 0, 2) // third clone -> EMFILE
	isError(t, resp, proto.EMFILE)
}

// TestMaxFids_WalkMultiEMFILE verifies that a multi-name Twalk at the fid
// cap returns Rlerror{EMFILE} and NOT a partial Rwalk with QIDs.
// Pitfall 3 defensive assertion: "not an Rwalk".
func TestMaxFids_WalkMultiEMFILE(t *testing.T) {
	t.Parallel()
	root := testTree()
	cp := newConnPair(t, root, WithMaxFids(2))
	t.Cleanup(func() { cp.close(t) })

	cp.attach(t, 1, 0, "u", "") // 1/2
	resp := cp.walk(t, 2, 0, 1) // clone -> 2/2
	if _, ok := resp.(*proto.Rwalk); !ok {
		t.Fatalf("expected Rwalk for clone, got %T: %+v", resp, resp)
	}
	// Multi-name walk at cap must fail with EMFILE, not return a partial Rwalk.
	resp = cp.walk(t, 3, 0, 2, "sub")
	if _, ok := resp.(*proto.Rwalk); ok {
		t.Fatalf("got Rwalk when EMFILE expected (partial-walk contract broken): %+v", resp)
	}
	isError(t, resp, proto.EMFILE)
}

// TestMaxFids_XattrwalkEMFILE verifies that a Txattrwalk at the fid cap
// returns Rlerror{EMFILE} and NOT Rxattrwalk{Size:0}.
// Pitfall 4 defensive assertion: "not an Rxattrwalk".
//
// The root node must implement at least one xattr interface so the cap
// check (which runs AFTER the interface dispatch in handleXattrwalk) is
// reached. We use xattrFile as root -- it satisfies NodeXattrLister,
// so Txattrwalk with Name="" enters the list-mode branch and calls add().
func TestMaxFids_XattrwalkEMFILE(t *testing.T) {
	t.Parallel()
	root := &xattrFile{xattrs: map[string][]byte{"user.color": []byte("red")}}
	root.Init(proto.QID{Type: proto.QTFILE, Path: 1}, root)
	cp := newConnPair(t, root, WithMaxFids(1))
	t.Cleanup(func() { cp.close(t) })

	cp.attach(t, 1, 0, "u", "") // 1/1

	sendMessage(t, cp.client, 2, &p9l.Txattrwalk{
		Fid:    0,
		NewFid: 1,
		Name:   "",
	})
	_, msg := readResponse(t, cp.client)
	// Must be Rlerror{EMFILE}, NOT Rxattrwalk{Size: 0}.
	if _, ok := msg.(*p9l.Rxattrwalk); ok {
		t.Fatalf("got Rxattrwalk when EMFILE expected (Pitfall 4 regression): %+v", msg)
	}
	isError(t, msg, proto.EMFILE)
}

// TestMaxFids_ClunkFreesSlot verifies that Tclunk releases a fid slot:
// an attach that previously hit EMFILE succeeds after the existing fid
// is clunked.
func TestMaxFids_ClunkFreesSlot(t *testing.T) {
	t.Parallel()
	root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})
	cp := newConnPair(t, root, WithMaxFids(1))
	t.Cleanup(func() { cp.close(t) })

	cp.attach(t, 1, 0, "u", "")               // 1/1
	msg := sendAttachExpectError(t, cp, 2, 1) // 2/1 -> EMFILE
	isError(t, msg, proto.EMFILE)

	resp := cp.clunk(t, 3, 0) // free slot
	if _, ok := resp.(*proto.Rclunk); !ok {
		t.Fatalf("expected Rclunk, got %T: %+v", resp, resp)
	}

	// Now fid 1 should succeed.
	cp.attach(t, 4, 1, "u", "")
}

// TestMaxFids_ClonedFidCoversCap verifies that cloned fids count against
// the cap identically to attached fids.
func TestMaxFids_ClonedFidCoversCap(t *testing.T) {
	t.Parallel()
	root := testTree()
	cp := newConnPair(t, root, WithMaxFids(2))
	t.Cleanup(func() { cp.close(t) })

	cp.attach(t, 1, 0, "u", "") // 1/2
	resp := cp.walk(t, 2, 0, 1) // clone -> 2/2
	if _, ok := resp.(*proto.Rwalk); !ok {
		t.Fatalf("expected Rwalk for clone, got %T: %+v", resp, resp)
	}
	resp = cp.walk(t, 3, 0, 2) // third -> EMFILE
	isError(t, resp, proto.EMFILE)
}

// TestLimits_CombinedConfig exercises both WithMaxConnections(2) and
// WithMaxFids(3) on the same server, validating that the two caps are
// independent and do not interfere with each other.
//
// Each connection owns its own context.WithCancel(testCtx) so closing one
// does not cascade-cancel the others. t.Cleanup waits on each server
// goroutine's done channel -- liveness is enforced without relying on the
// outer test timeout.
func TestLimits_CombinedConfig(t *testing.T) {
	t.Parallel()
	root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})

	// Server: accept 2 connections, each with 3 fids.
	srv := New(root, WithMaxConnections(2), WithMaxFids(3), WithLogger(discardLogger()))

	testCtx, testCancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(testCancel)

	// openConn opens a connection with its OWN context so per-conn teardown
	// does not cascade. Each returned connPair's cleanup waits on the
	// ServeConn goroutine's done channel.
	openConn := func() *connPair {
		cc, sc := net.Pipe()
		connCtx, connCancel := context.WithCancel(testCtx)
		done := make(chan struct{})
		go func() { defer close(done); srv.ServeConn(connCtx, sc) }()
		sendTversion(t, cc, 65536, "9P2000.L")
		_ = readRversion(t, cc)
		t.Cleanup(func() {
			_ = cc.Close()
			connCancel()
			<-done // wait for ServeConn goroutine to exit
		})
		return &connPair{client: cc, done: done, cancel: connCancel}
	}

	// First connection: attach + 2 clones = 3 fids (at cap).
	cp1 := openConn()
	cp1.attach(t, 1, 0, "u", "") // 1/3
	if _, ok := cp1.walk(t, 2, 0, 1).(*proto.Rwalk); !ok {
		t.Fatalf("cp1 clone 1 failed")
	} // 2/3
	if _, ok := cp1.walk(t, 3, 0, 2).(*proto.Rwalk); !ok {
		t.Fatalf("cp1 clone 2 failed")
	} // 3/3
	// 4th fid on cp1 -> EMFILE.
	resp := cp1.walk(t, 4, 0, 3)
	isError(t, resp, proto.EMFILE)

	// Second connection: accepted, independent ctx from cp1. Its own fid
	// budget is independent of cp1's -- attach must succeed despite cp1
	// being at its cap.
	cp2 := openConn()
	cp2.attach(t, 1, 0, "u", "")

	// Third connection: rejected by the connection cap. Its ctx/cancel
	// are independent so we can wait on done without relying on testCtx.
	c3Client, c3Server := net.Pipe()
	c3Ctx, c3Cancel := context.WithCancel(testCtx)
	t.Cleanup(func() { c3Cancel(); _ = c3Client.Close() })
	done3 := make(chan struct{})
	go func() { defer close(done3); srv.ServeConn(c3Ctx, c3Server) }()
	select {
	case <-done3:
		// ok -- server rejected and returned
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("3rd ServeConn did not return; conn-cap not enforced")
	}
}

// TestLimits_ConcurrentWalkClunkStress runs G goroutines, each owning its
// own dedicated connPair, doing N iterations of walk+clunk under
// WithMaxFids(5). Each goroutine owns a single connection exclusively --
// no cross-goroutine wire interleaving -- so this test exercises
// server-side concurrency: G parallel ServeConn goroutines sharing one
// Server, each with its own fidTable, contending on fidTable.add's cap
// branch under -race.
//
// Per-conn fid budget is 5 (fid 0 = attach, fids 1..4 cycle through
// walk+clunk). The tight clone+clunk loop never hits the cap, so every
// walk MUST succeed. A returned EMFILE would indicate cross-connection
// state leakage -- a bug.
func TestLimits_ConcurrentWalkClunkStress(t *testing.T) {
	t.Parallel()
	root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})

	const G = 4  // goroutines (and connections)
	const N = 50 // iterations per goroutine

	var wg sync.WaitGroup
	errs := make(chan error, G*N*2)
	for g := range G {
		wg.Add(1)
		go func(gID int) {
			defer wg.Done()
			// Each goroutine owns its connection exclusively -- sendMessage
			// and readResponse pairs never interleave across goroutines.
			cp := newConnPair(t, root, WithMaxFids(5))
			defer cp.close(t)

			// Attach fid 0 on this connection (1/5).
			cp.attach(t, proto.Tag(gID*10_000+1), 0, "u", "")

			for i := range N {
				// Reuse a small set of fids; clunk immediately after clone
				// so this connection never reaches the per-conn cap.
				newFid := proto.Fid(1 + (i % 4))
				tag := proto.Tag(gID*10_000 + 100 + i)

				resp := cp.walk(t, tag, 0, newFid)
				if _, ok := resp.(*proto.Rwalk); !ok {
					errs <- fmt.Errorf("g%d i%d: walk got %T, want *proto.Rwalk", gID, i, resp)
					return
				}
				cresp := cp.clunk(t, proto.Tag(int(tag)+1), newFid)
				if _, ok := cresp.(*proto.Rclunk); !ok {
					errs <- fmt.Errorf("g%d i%d: clunk got %T, want *proto.Rclunk", gID, i, cresp)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("goroutine error: %v", err)
	}
}

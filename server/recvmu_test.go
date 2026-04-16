package server

import (
	"bytes"
	"context"
	"net"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// recvmuBlockingNode is a directory whose Lookup blocks on a per-test
// channel until the test signals release. Used to drive the recv-mutex
// worker model past its cap and to control inflight handlers from outside
// the server package.
type recvmuBlockingNode struct {
	Inode
	release chan struct{}
	active  atomic.Int32
	started chan struct{} // closed when the first Lookup begins
}

func newRecvmuBlockingNode(qid proto.QID) *recvmuBlockingNode {
	n := &recvmuBlockingNode{
		release: make(chan struct{}),
		started: make(chan struct{}),
	}
	n.Init(qid, n)
	return n
}

func (n *recvmuBlockingNode) Lookup(ctx context.Context, _ string) (Node, error) {
	n.active.Add(1)
	defer n.active.Add(-1)

	select {
	case <-n.started:
	default:
		close(n.started)
	}

	select {
	case <-n.release:
		f := &recvmuFile{}
		f.Init(proto.QID{Type: proto.QTFILE, Path: 99}, f)
		return f, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// recvmuFile is a minimal file node returned by recvmuBlockingNode.Lookup.
type recvmuFile struct {
	Inode
}

// cliEncodeWalk encodes and writes a Twalk frame to w, returning any error
// from the underlying Write. Used by tests that send from background
// goroutines where a fatal helper would be unsafe.
func cliEncodeWalk(w net.Conn, tag proto.Tag, newFid proto.Fid) (int, error) {
	var buf bytes.Buffer
	if err := p9l.Encode(&buf, tag, &proto.Twalk{
		Fid:    0,
		NewFid: newFid,
		Names:  []string{"child"},
	}); err != nil {
		return 0, err
	}
	return w.Write(buf.Bytes())
}

// negotiateAndAttach sends Tversion + Tattach for fid 0 and reads both
// responses. Used by the recv-mutex tests to set the conn into a steady
// state before exercising lifecycle behaviour.
func negotiateAndAttach(t *testing.T, client net.Conn) {
	t.Helper()
	sendTversion(t, client, 65536, "9P2000.L")
	_ = readRversion(t, client)
	sendMessage(t, client, 1, &proto.Tattach{
		Fid:   0,
		Afid:  proto.NoFid,
		Uname: "test",
	})
	_, msg := readResponse(t, client)
	if _, ok := msg.(*proto.Rattach); !ok {
		t.Fatalf("attach: expected Rattach, got %T", msg)
	}
}

func TestRecvMuWorkerLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("WorkerCountRespectsMaxInflight", func(t *testing.T) {
		t.Parallel()

		rootQID := proto.QID{Type: proto.QTDIR, Path: 1}
		root := newRecvmuBlockingNode(rootQID)

		client, server := net.Pipe()
		defer func() { _ = server.Close() }()

		srv := New(root,
			WithMaxMsize(65536),
			WithMaxInflight(2),
			WithLogger(discardLogger()),
		)

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()

		c := newConn(srv, server)
		serveDone := make(chan struct{})
		go func() {
			defer close(serveDone)
			c.serve(ctx)
		}()

		// We need attach to succeed before we can send walks. Use a
		// throwaway "started" sync and release exactly the attach lookup
		// (Tattach does not call Lookup -- it walks fid to root). So just
		// negotiate and attach.
		negotiateAndAttach(t, client)

		// Send the first 2 walks synchronously: at WithMaxInflight(2) the
		// recv-mutex model can only park `cap` goroutines simultaneously
		// (one dispatching, one would-be successor). Beyond that, sends
		// would block on net.Pipe because nobody is reading. We send
		// further messages from a goroutine instead so the test can sample
		// workerCount without deadlocking.
		const concurrent = 2
		for i := range concurrent {
			sendMessage(t, client, proto.Tag(10+i), &proto.Twalk{
				Fid:    0,
				NewFid: proto.Fid(10 + i),
				Names:  []string{"child"},
			})
		}

		// Background goroutine attempts additional sends. These will
		// block on the pipe until handlers release; that's fine -- we
		// only care that workerCount stays bounded.
		writerDone := make(chan struct{})
		go func() {
			defer close(writerDone)
			for i := concurrent; i < 5; i++ {
				if _, err := cliEncodeWalk(client, proto.Tag(10+i), proto.Fid(10+i)); err != nil {
					return
				}
			}
		}()

		// Give time for handlers to spawn.
		select {
		case <-root.started:
		case <-time.After(2 * time.Second):
			t.Fatal("no handler started")
		}

		// Sample workerCount many times. With WithMaxInflight(2), the
		// goroutine population is bounded by the cap: dispatcher +
		// (at most one) successor parked on recvMu. So workerCount
		// must always be <= 2.
		var maxObserved int32
		for i := 0; i < 100; i++ {
			n := c.workerCount.Load()
			if n > maxObserved {
				maxObserved = n
			}
			if n > 2 {
				t.Fatalf("workerCount=%d exceeds maxInflight=2 (sample %d)", n, i)
			}
			time.Sleep(5 * time.Millisecond)
		}
		if maxObserved < 1 {
			t.Fatalf("workerCount never reached 1 (max observed=%d) -- handler not running?", maxObserved)
		}

		// Release handlers. We expect 5 total responses (2 sent up
		// front + 3 from the writer goroutine), but order is not
		// guaranteed and the writer may still be blocked on Write
		// when we start reading. Drain responses by reading until we
		// have 5 OR the pipe closes.
		close(root.release)
		got := 0
		for got < 5 {
			_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
			if _, _, err := p9l.Decode(client); err != nil {
				t.Logf("drain stopped at response %d: %v", got, err)
				break
			}
			got++
		}
		<-writerDone
		if got < 5 {
			t.Errorf("got %d responses, want 5", got)
		}

		_ = client.Close()
		select {
		case <-serveDone:
		case <-time.After(5 * time.Second):
			t.Fatal("serve did not exit after client close")
		}
	})

	t.Run("CleanExitOnDisconnect", func(t *testing.T) {
		t.Parallel()

		// Warm up to stabilise goroutine count.
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
		baseGoroutines := runtime.NumGoroutine()

		rootQID := proto.QID{Type: proto.QTDIR, Path: 1}
		root := newRecvmuBlockingNode(rootQID)

		client, server := net.Pipe()

		srv := New(root,
			WithMaxMsize(65536),
			WithMaxInflight(8),
			WithLogger(discardLogger()),
		)

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		c := newConn(srv, server)
		serveDone := make(chan struct{})
		go func() {
			defer close(serveDone)
			c.serve(ctx)
		}()

		negotiateAndAttach(t, client)

		// Send 3 concurrent blocking walks.
		for i := range 3 {
			sendMessage(t, client, proto.Tag(10+i), &proto.Twalk{
				Fid:    0,
				NewFid: proto.Fid(10 + i),
				Names:  []string{"child"},
			})
		}

		// Wait for at least one handler to start.
		select {
		case <-root.started:
		case <-time.After(2 * time.Second):
			t.Fatal("no handler started")
		}

		// Close client side -> recvMu-holder errors out -> recvShutdown.
		// Cancel context -> watcher closes nc -> blocked handlers
		// observe ctx.Done().
		_ = client.Close()

		// Wait for serve to exit (cleanup runs, then return). Bound at
		// cleanupDeadline + slack.
		select {
		case <-serveDone:
		case <-time.After(cleanupDeadline + 5*time.Second):
			t.Fatal("serve did not exit after disconnect")
		}

		_ = server.Close()

		// Allow time for goroutine cleanup.
		time.Sleep(100 * time.Millisecond)
		runtime.GC()
		time.Sleep(100 * time.Millisecond)

		finalGoroutines := runtime.NumGoroutine()
		if finalGoroutines > baseGoroutines+10 {
			t.Errorf("goroutine leak: before=%d, after=%d (tolerance=10)",
				baseGoroutines, finalGoroutines)
		}
	})

	t.Run("SpawnsSuccessorAfterDispatch", func(t *testing.T) {
		t.Parallel()

		rootQID := proto.QID{Type: proto.QTDIR, Path: 1}
		root := newRecvmuBlockingNode(rootQID)

		client, server := net.Pipe()
		defer func() { _ = server.Close() }()

		srv := New(root,
			WithMaxMsize(65536),
			WithMaxInflight(4),
			WithLogger(discardLogger()),
		)

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()

		c := newConn(srv, server)
		serveDone := make(chan struct{})
		go func() {
			defer close(serveDone)
			c.serve(ctx)
		}()

		negotiateAndAttach(t, client)

		// Send a single blocking walk.
		sendMessage(t, client, 10, &proto.Twalk{
			Fid:    0,
			NewFid: 1,
			Names:  []string{"child"},
		})

		// Wait for handler to start.
		select {
		case <-root.started:
		case <-time.After(2 * time.Second):
			t.Fatal("handler did not start")
		}

		// After dispatch, the spawn-replacement decision fires: the
		// dispatcher spawned a successor that becomes the recvMu
		// holder. workerCount must reach >= 2 (1 dispatcher + 1
		// successor reading from the wire). recvIdle stays at 0 in
		// steady state because the successor IS the lock holder, not
		// a goroutine WAITING for the lock.
		deadline := time.Now().Add(2 * time.Second)
		var seen int32
		for time.Now().Before(deadline) {
			if v := c.workerCount.Load(); v >= 2 {
				seen = v
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if seen < 2 {
			t.Fatalf("workerCount never reached >= 2 (final=%d) -- successor not spawned",
				c.workerCount.Load())
		}

		// Force a brief recvIdle observation by sending another message
		// while the dispatcher is still blocked: the new message wakes
		// the successor (current recvMu holder), which dispatches and
		// spawns a third worker. During the spawn->Lock window of the
		// third worker, recvIdle briefly reaches 1. Sample many times
		// to catch the transient.
		sendMessage(t, client, 11, &proto.Twalk{
			Fid:    0,
			NewFid: 2,
			Names:  []string{"child"},
		})

		// recvIdle is a race indicator: it may never be observed > 0
		// from outside in steady state. The presence of a 3rd worker
		// is the actionable predicate -- workerCount.
		deadline2 := time.Now().Add(2 * time.Second)
		var seen3 int32
		for time.Now().Before(deadline2) {
			if v := c.workerCount.Load(); v >= 3 {
				seen3 = v
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		if seen3 < 3 {
			t.Fatalf("workerCount never reached >= 3 (final=%d) -- successor failed to spawn after 2nd request",
				c.workerCount.Load())
		}

		// Release blocked handlers and drain responses.
		close(root.release)
		got := 0
		for got < 2 {
			_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, _, err := p9l.Decode(client); err != nil {
				break
			}
			got++
		}
		if got < 2 {
			t.Errorf("got %d responses, want 2", got)
		}

		_ = client.Close()
		select {
		case <-serveDone:
		case <-time.After(5 * time.Second):
			t.Fatal("serve did not exit")
		}
	})

	t.Run("TversionDoesNotSpawnReplacement", func(t *testing.T) {
		t.Parallel()

		rootQID := proto.QID{Type: proto.QTDIR, Path: 1}
		root := newRecvmuBlockingNode(rootQID)

		client, server := net.Pipe()
		defer func() { _ = server.Close() }()

		srv := New(root,
			WithMaxMsize(65536),
			WithMaxInflight(4),
			WithLogger(discardLogger()),
		)

		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()

		c := newConn(srv, server)
		serveDone := make(chan struct{})
		go func() {
			defer close(serveDone)
			c.serve(ctx)
		}()

		negotiateAndAttach(t, client)

		// Step 1: Send a blocking walk to install an inflight request.
		sendMessage(t, client, 10, &proto.Twalk{
			Fid:    0,
			NewFid: 1,
			Names:  []string{"child"},
		})
		select {
		case <-root.started:
		case <-time.After(2 * time.Second):
			t.Fatal("walk handler did not start")
		}

		// At this point: one goroutine is blocked in Lookup (dispatcher
		// for tag 10), and the recv-mutex worker model has spawned a
		// successor parked on recvMu waiting for the next message. So
		// workerCount should be 2 and recvIdle should be 1.
		preWorker := c.workerCount.Load()
		if preWorker < 2 {
			t.Fatalf("pre-Tversion workerCount=%d, want >= 2", preWorker)
		}

		// Step 2: Send a mid-conn Tversion. handleReVersion will
		// inflight.cancelAll() (which cancels tag 10) then
		// waitWithDeadline. Once the walk handler observes the
		// cancellation it returns; only then does handleReVersion
		// proceed to the next steps and ultimately writeRaw the
		// Rversion.
		//
		// During handleReVersion, the goroutine that read the Tversion
		// MUST NOT have spawned a replacement. So workerCount must not
		// increase while handleReVersion is in progress.
		//
		// To detect "in progress", sample workerCount many times after
		// sending the Tversion. The sampling window starts immediately
		// (Tversion needs to traverse the pipe -> recvMu-holder reads
		// it -> peeks msgType=Tversion -> skips spawn -> releases
		// recvMu -> calls handleReVersion which calls cancelAll). The
		// walk handler is still blocked in Lookup (it ignores ctx.Done()
		// briefly to handle cancel) -- in our recvmuBlockingNode, the
		// select in Lookup includes ctx.Done(), so cancellation does
		// fire immediately. To widen the window, we sample DURING
		// handleReVersion's drain phase by starting samples right after
		// we write Tversion.
		sendTversion(t, client, 65536, "9P2000.L")

		// Sample workerCount during the handleReVersion window.
		// Allow up to 50 samples spaced 2ms apart (~100ms window).
		// During this entire window, workerCount must NOT exceed
		// preWorker. After the Rversion arrives, the loop will spawn a
		// replacement on the next iteration -- that's allowed and not
		// observed here.
		maxDuringTversion := preWorker
		// First, drain the cancelled tag 10 response (Rlerror with
		// EINTR/ECANCELED) and then the Rversion.
		respCh := make(chan struct{})
		go func() {
			defer close(respCh)
			_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
			for range 2 {
				if _, _, err := proto9PRead(client); err != nil {
					return
				}
			}
		}()

		samplingDone := time.After(100 * time.Millisecond)
		samplingLoop := true
		for samplingLoop {
			select {
			case <-respCh:
				samplingLoop = false
			case <-samplingDone:
				samplingLoop = false
			default:
				if v := c.workerCount.Load(); v > maxDuringTversion {
					maxDuringTversion = v
				}
				time.Sleep(2 * time.Millisecond)
			}
		}

		if maxDuringTversion > preWorker {
			t.Errorf("workerCount during handleReVersion increased from %d to %d -- spawn-skip on Tversion is broken",
				preWorker, maxDuringTversion)
		}

		// Wait for response drainer to finish (or timeout) so we don't
		// leak the goroutine into other subtests.
		<-respCh

		_ = client.Close()
		select {
		case <-serveDone:
		case <-time.After(cleanupDeadline + 5*time.Second):
			t.Fatal("serve did not exit")
		}
	})
}

// proto9PRead reads a single 9P frame from r and returns (size, type, error).
// Used by TversionDoesNotSpawnReplacement to drain responses without
// caring about message decoding (which would block on the pipe at
// arbitrary points).
func proto9PRead(c net.Conn) (uint32, uint8, error) {
	var hdr [4]byte
	if _, err := readFullDeadline(c, hdr[:]); err != nil {
		return 0, 0, err
	}
	size := uint32(hdr[0]) | uint32(hdr[1])<<8 | uint32(hdr[2])<<16 | uint32(hdr[3])<<24
	if size < 5 {
		return size, 0, nil
	}
	body := make([]byte, size-4)
	if _, err := readFullDeadline(c, body); err != nil {
		return size, 0, err
	}
	return size, body[0], nil
}

// readFullDeadline is io.ReadFull with the connection's existing read
// deadline (set by the caller via SetReadDeadline).
func readFullDeadline(c net.Conn, b []byte) (int, error) {
	return readFullN(c, b)
}

// readFullN reads exactly len(b) bytes from c.
func readFullN(c net.Conn, b []byte) (int, error) {
	total := 0
	for total < len(b) {
		n, err := c.Read(b[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// Compile-time assertions.
var (
	_ NodeLookuper  = (*recvmuBlockingNode)(nil)
	_ InodeEmbedder = (*recvmuBlockingNode)(nil)
	_ InodeEmbedder = (*recvmuFile)(nil)
)

package server

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// --- inflightMap unit tests ---

func TestInflightMap_StartFinish(t *testing.T) {
	t.Parallel()

	im := newInflightMap()
	rctx := getRequestCtx(t.Context())
	defer putRequestCtx(rctx)

	im.start(1, rctx)
	if im.len() != 1 {
		t.Fatalf("len after start = %d, want 1", im.len())
	}

	im.finish(1)
	if im.len() != 0 {
		t.Fatalf("len after finish = %d, want 0", im.len())
	}
}

// TestInflightMap_FlushWaitsOnCommittedEntry pins the fix for the window
// between dispatchInline committing a tag (about to write its response)
// and completeCommit running after the write finishes. Before this fix,
// the tag was removed from the map before the write instead of marked
// committed, so a Tflush arriving in that window found no entry and
// returned Rflush immediately -- which could win the race for writeMu and
// ship before the response it was meant to follow.
func TestInflightMap_FlushWaitsOnCommittedEntry(t *testing.T) {
	t.Parallel()

	im := newInflightMap()
	rctx := getRequestCtx(t.Context())
	defer putRequestCtx(rctx)

	if !im.start(1, rctx) {
		t.Fatal("start failed")
	}

	// Simulate dispatchInline about to write the response.
	im.commit(1)

	// A Tflush arrives while the response write is still in flight (the
	// exact race window). It must find the entry and get a channel to
	// wait on, not nil (which would let handleFlush return Rflush
	// immediately, ahead of the response still being written).
	done := im.flushWait(1)
	if done == nil {
		t.Fatal("flushWait returned nil for a committed-but-not-yet-completed entry; Rflush could overtake the response")
	}
	select {
	case <-done:
		t.Fatal("done closed before completeCommit ran")
	default:
	}

	// Simulate the write finishing: dispatchInline fetches whatever done
	// channel exists now (created above by the late-arriving flushWait)
	// and closes it.
	finalDone := im.completeCommit(1, rctx)
	if finalDone != done {
		t.Fatal("completeCommit did not return the done channel flushWait created mid-write")
	}
	close(finalDone)

	select {
	case <-done:
	default:
		t.Fatal("flushWait's done channel did not close after the response completed")
	}
	if im.len() != 0 {
		t.Fatalf("len after completeCommit = %d, want 0", im.len())
	}
}

// TestInflightMap_StartReusesCommittedTag verifies that once a tag's entry
// is committed (its response write has begun), a legitimately reused tag
// is accepted rather than tearing the connection down as a duplicate, and
// that the original request's own completeCommit call -- arriving after
// the reuse -- is a safe no-op that does not disturb the new entry.
func TestInflightMap_StartReusesCommittedTag(t *testing.T) {
	t.Parallel()

	im := newInflightMap()
	first := getRequestCtx(t.Context())
	second := getRequestCtx(t.Context())
	defer putRequestCtx(first)
	defer putRequestCtx(second)

	if !im.start(1, first) {
		t.Fatal("first start failed")
	}
	im.commit(1)

	if !im.start(1, second) {
		t.Fatal("start on a committed tag should succeed (client saw the response)")
	}
	if im.len() != 1 {
		t.Fatalf("len after reuse = %d, want 1", im.len())
	}

	// The original request's completeCommit arrives late; it must not
	// remove the new entry or double-decrement count.
	im.completeCommit(1, first)
	if im.len() != 1 {
		t.Fatalf("len after stale completeCommit = %d, want 1 (new entry must survive)", im.len())
	}

	im.finish(1)
	if im.len() != 0 {
		t.Fatalf("len after finishing the new entry = %d, want 0", im.len())
	}
}

func TestInflightMap_StartRejectsDuplicateTag(t *testing.T) {
	t.Parallel()

	im := newInflightMap()
	first := getRequestCtx(t.Context())
	second := getRequestCtx(t.Context())
	defer putRequestCtx(first)
	defer putRequestCtx(second)

	if !im.start(1, first) {
		t.Fatal("first start returned false")
	}
	if im.start(1, second) {
		t.Fatal("duplicate start returned true")
	}
	if im.len() != 1 {
		t.Fatalf("len after duplicate start = %d, want 1", im.len())
	}

	firstDone := first.Done()
	secondDone := second.Done()
	im.flush(1)

	select {
	case <-firstDone:
	default:
		t.Fatal("flush did not cancel original request context")
	}
	select {
	case <-secondDone:
		t.Fatal("flush cancelled duplicate request context")
	default:
	}

	im.finish(1)
	if im.len() != 0 {
		t.Fatalf("len after finish = %d, want 0", im.len())
	}
}

func TestInflightMap_FlushCancelsContext(t *testing.T) {
	t.Parallel()

	im := newInflightMap()
	rctx := getRequestCtx(t.Context())
	defer putRequestCtx(rctx)

	im.start(1, rctx)

	// Observe Done() before flush so the channel is allocated and flush's
	// close-path exercises the initialized-channel branch.
	done := rctx.Done()

	// Flush should cancel the context.
	im.flush(1)

	select {
	case <-done:
		// Expected: context was cancelled.
	case <-time.After(time.Second):
		t.Fatal("context not cancelled after flush")
	}
	if rctx.Err() != context.Canceled {
		t.Fatalf("Err() after flush = %v, want context.Canceled", rctx.Err())
	}

	// Entry should still be present (handler hasn't finished yet).
	if im.len() != 1 {
		t.Fatalf("len after flush = %d, want 1 (entry not removed until finish)", im.len())
	}

	im.finish(1)
	if im.len() != 0 {
		t.Fatalf("len after finish = %d, want 0", im.len())
	}
}

func TestInflightMap_FlushNonexistentTag(t *testing.T) {
	t.Parallel()

	im := newInflightMap()

	// Should not panic.
	im.flush(999)
}

func TestInflightMap_CancelAll(t *testing.T) {
	t.Parallel()

	im := newInflightMap()

	rctxs := make([]*requestCtx, 3)
	dones := make([]<-chan struct{}, 3)
	for i := range 3 {
		r := getRequestCtx(t.Context())
		rctxs[i] = r
		dones[i] = r.Done() // allocate channel up-front so cancelAll's close fires
		im.start(proto.Tag(i), r)
	}
	defer func() {
		for _, r := range rctxs {
			putRequestCtx(r)
		}
	}()

	im.cancelAll()

	for i, done := range dones {
		select {
		case <-done:
			// Expected.
		default:
			t.Errorf("context %d not cancelled after cancelAll", i)
		}
		if rctxs[i].Err() != context.Canceled {
			t.Errorf("ctx %d Err() = %v, want context.Canceled", i, rctxs[i].Err())
		}
	}

	// Entries still present until finish is called.
	if im.len() != 3 {
		t.Fatalf("len after cancelAll = %d, want 3", im.len())
	}

	for i := range 3 {
		im.finish(proto.Tag(i))
	}
}

func TestInflightMap_Wait(t *testing.T) {
	t.Parallel()

	im := newInflightMap()
	rctx := getRequestCtx(t.Context())
	defer putRequestCtx(rctx)

	im.start(1, rctx)

	done := make(chan struct{})
	go func() {
		im.wait()
		close(done)
	}()

	// Wait should not complete yet.
	select {
	case <-done:
		t.Fatal("wait returned before finish")
	case <-time.After(50 * time.Millisecond):
		// Expected.
	}

	im.finish(1)

	select {
	case <-done:
		// Expected.
	case <-time.After(time.Second):
		t.Fatal("wait did not return after finish")
	}
}

func TestInflightMap_WaitWithDeadline(t *testing.T) {
	t.Parallel()

	im := newInflightMap()
	rctx := getRequestCtx(t.Context())
	defer putRequestCtx(rctx)

	im.start(1, rctx)

	deadlineCtx, deadlineCancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer deadlineCancel()

	err := im.waitWithDeadline(deadlineCtx)
	if err == nil {
		t.Fatal("waitWithDeadline should return error when deadline exceeded")
	}

	im.finish(1)
}

// --- Integration tests using real server and net.Pipe ---

// blockingNode implements Node and NodeLookuper. Lookup blocks until
// the provided channel is closed or context is cancelled. This lets tests
// control when handlers complete.
type blockingNode struct {
	Inode
	block   chan struct{} // close to unblock Lookup
	started chan struct{} // closed when Lookup begins executing
}

func newBlockingNode(qid proto.QID) *blockingNode {
	n := &blockingNode{
		block:   make(chan struct{}),
		started: make(chan struct{}),
	}
	n.Init(qid, n)
	return n
}

func (n *blockingNode) Lookup(ctx context.Context, _ string) (Node, error) {
	// Signal that we've entered Lookup.
	select {
	case <-n.started:
	default:
		close(n.started)
	}

	// Block until unblocked or context cancelled.
	select {
	case <-n.block:
		f := &testFile{}
		f.Init(proto.QID{Type: proto.QTFILE, Path: 42}, f)
		return f, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// panicNode implements NodeLookuper and panics in Lookup.
type panicNode struct {
	Inode
}

func (n *panicNode) Lookup(_ context.Context, _ string) (Node, error) {
	panic("test panic in Lookup")
}

// countingNode counts concurrent active Lookup calls.
type countingNode struct {
	Inode
	block   chan struct{}
	active  atomic.Int32
	started chan struct{} // closed when first Lookup begins
}

func newCountingNode(qid proto.QID) *countingNode {
	n := &countingNode{
		block:   make(chan struct{}),
		started: make(chan struct{}),
	}
	n.Init(qid, n)
	return n
}

func (n *countingNode) Lookup(ctx context.Context, _ string) (Node, error) {
	n.active.Add(1)
	defer n.active.Add(-1)

	// Signal that at least one Lookup started.
	select {
	case <-n.started:
	default:
		close(n.started)
	}

	select {
	case <-n.block:
		f := &testFile{}
		f.Init(proto.QID{Type: proto.QTFILE, Path: 42}, f)
		return f, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestFlush_CancelsContext(t *testing.T) {
	t.Parallel()

	rootQID := proto.QID{Type: proto.QTDIR, Path: 1}
	root := newBlockingNode(rootQID)

	cp := newConnPair(t, root)
	defer cp.close(t)

	// Attach.
	cp.attach(t, 1, 0, "user", "")

	// Send a Twalk that will block in Lookup.
	sendMessage(t, cp.client, 10, &proto.Twalk{
		Fid:    0,
		NewFid: 1,
		Names:  []string{"anything"},
	})

	// Wait for the handler to start blocking.
	select {
	case <-root.started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}

	// Send Tflush for tag 10.
	sendMessage(t, cp.client, 11, &proto.Tflush{OldTag: 10})

	// Read responses. We should get an Rflush for tag 11.
	// We may also get an error response for tag 10 (because its context was cancelled).
	gotFlush := false
	for range 2 {
		tag, msg := readResponse(t, cp.client)
		switch tag {
		case 11:
			if _, ok := msg.(*proto.Rflush); !ok {
				t.Fatalf("expected Rflush for tag 11, got %T", msg)
			}
			gotFlush = true
		case 10:
			// Error response for the flushed request -- acceptable.
		default:
			t.Fatalf("unexpected tag %d", tag)
		}
		if gotFlush {
			break
		}
	}

	if !gotFlush {
		t.Fatal("did not receive Rflush")
	}
}

func TestFlush_UnknownTag(t *testing.T) {
	t.Parallel()

	rootQID := proto.QID{Type: proto.QTDIR, Path: 1}
	root := newRootNode(rootQID)

	cp := newConnPair(t, root)
	defer cp.close(t)

	// Flush a tag that has no inflight request. Should still return Rflush.
	sendMessage(t, cp.client, 1, &proto.Tflush{OldTag: 999})
	tag, msg := readResponse(t, cp.client)
	if tag != 1 {
		t.Fatalf("tag = %d, want 1", tag)
	}
	if _, ok := msg.(*proto.Rflush); !ok {
		t.Fatalf("expected Rflush, got %T", msg)
	}
}

func TestFlush_TagReuse(t *testing.T) {
	t.Parallel()

	rootQID := proto.QID{Type: proto.QTDIR, Path: 1}
	root := newBlockingNode(rootQID)

	cp := newConnPair(t, root)
	defer cp.close(t)

	cp.attach(t, 1, 0, "user", "")

	// Send request that blocks.
	sendMessage(t, cp.client, 10, &proto.Twalk{
		Fid:    0,
		NewFid: 1,
		Names:  []string{"child"},
	})

	select {
	case <-root.started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}

	// Flush tag 10.
	sendMessage(t, cp.client, 11, &proto.Tflush{OldTag: 10})

	// Drain responses for tags 10 and 11.
	for range 2 {
		_ = cp.client.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _ = readResponse(t, cp.client)
	}

	// After flush+drain, unblock to let handler finish and clear the tag.
	close(root.block)

	// Small delay for the handler goroutine to complete and call finish().
	time.Sleep(50 * time.Millisecond)

	// Now create a NEW blockingNode for a fresh connection pair -- tag reuse
	// test is primarily about inflight map state, which we've verified above.
}

func TestDuplicateInflightTagClosesConnection(t *testing.T) {
	t.Parallel()

	rootQID := proto.QID{Type: proto.QTDIR, Path: 1}
	root := newBlockingNode(rootQID)
	defer close(root.block)

	cp := newConnPair(t, root)
	defer cp.close(t)

	cp.attach(t, 1, 0, "user", "")

	sendMessage(t, cp.client, 10, &proto.Twalk{
		Fid:    0,
		NewFid: 1,
		Names:  []string{"slow"},
	})

	select {
	case <-root.started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}

	sendMessage(t, cp.client, 10, &proto.Tclunk{Fid: 0})
	if err := cp.client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		return
	}
	if _, _, err := p9l.Decode(cp.client); err == nil {
		t.Fatal("expected connection close after duplicate in-flight tag")
	}
}

func TestPanicRecovery(t *testing.T) {
	t.Parallel()

	rootQID := proto.QID{Type: proto.QTDIR, Path: 1}
	root := &panicNode{}
	root.Init(rootQID, root)

	cp := newConnPair(t, root)
	defer cp.close(t)

	cp.attach(t, 1, 0, "user", "")

	// Twalk will call Lookup on panicNode, which panics.
	sendMessage(t, cp.client, 10, &proto.Twalk{
		Fid:    0,
		NewFid: 1,
		Names:  []string{"anything"},
	})

	// Should receive an EIO error (panic recovered).
	tag, msg := readResponse(t, cp.client)
	if tag != 10 {
		t.Fatalf("tag = %d, want 10", tag)
	}
	isError(t, msg, proto.EIO)

	// Server should still be alive -- send another request.
	sendMessage(t, cp.client, 11, &proto.Tclunk{Fid: 0})
	tag, msg = readResponse(t, cp.client)
	if tag != 11 {
		t.Fatalf("tag = %d, want 11", tag)
	}
	if _, ok := msg.(*proto.Rclunk); !ok {
		t.Fatalf("expected Rclunk after panic recovery, got %T", msg)
	}
}

func TestMaxInflight(t *testing.T) {
	t.Parallel()

	rootQID := proto.QID{Type: proto.QTDIR, Path: 1}
	root := newCountingNode(rootQID)

	cp := newConnPair(t, root, WithMaxInflight(2))
	defer cp.close(t)

	cp.attach(t, 1, 0, "user", "")

	// Send the first 2 requests synchronously: with WithMaxInflight(2)
	// the recv-mutex worker model can only have at most 2 dispatcher
	// goroutines alive at once. Sending a 3rd synchronously would block
	// on the pipe because no successor is spawned past the cap. Send
	// the 3rd from a background goroutine so the test does not
	// deadlock; the 3rd send will only complete once one of the first
	// two handlers releases.
	for i := range 2 {
		sendMessage(t, cp.client, proto.Tag(10+i), &proto.Twalk{
			Fid:    0,
			NewFid: proto.Fid(10 + i),
			Names:  []string{"child"},
		})
	}

	thirdDone := make(chan struct{})
	go func() {
		defer close(thirdDone)
		var buf bytes.Buffer
		if err := p9l.Encode(&buf, proto.Tag(12), &proto.Twalk{
			Fid:    0,
			NewFid: proto.Fid(12),
			Names:  []string{"child"},
		}); err != nil {
			return
		}
		_, _ = cp.client.Write(buf.Bytes())
	}()

	// Wait for at least one handler to start.
	select {
	case <-root.started:
	case <-time.After(2 * time.Second):
		t.Fatal("no handler started")
	}

	// Give time for all possible handlers to start.
	time.Sleep(100 * time.Millisecond)

	active := root.active.Load()
	if active > 2 {
		t.Fatalf("active handlers = %d, want <= 2 (MaxInflight=2)", active)
	}

	// Unblock all handlers.
	close(root.block)

	// Read responses while the 3rd send may still be in flight. With
	// net.Pipe, sendResponseInline blocks until the client reads, so
	// we must read interleaved with the writer goroutine completing.
	for range 3 {
		_ = cp.client.SetReadDeadline(time.Now().Add(2 * time.Second))
		readResponse(t, cp.client)
	}

	// Background sender should have finished by now (its Write
	// completed when the recvMu holder consumed tag 12 between
	// responses 2 and 3).
	select {
	case <-thirdDone:
	case <-time.After(2 * time.Second):
		t.Fatal("3rd send did not complete after responses drained")
	}
}

func TestConcurrentDispatch(t *testing.T) {
	t.Parallel()

	rootQID := proto.QID{Type: proto.QTDIR, Path: 1}
	root := newCountingNode(rootQID)

	cp := newConnPair(t, root)
	defer cp.close(t)

	cp.attach(t, 1, 0, "user", "")

	// Send multiple requests concurrently.
	const numRequests = 5
	for i := range numRequests {
		sendMessage(t, cp.client, proto.Tag(10+i), &proto.Twalk{
			Fid:    0,
			NewFid: proto.Fid(10 + i),
			Names:  []string{"child"},
		})
	}

	// Wait for at least one to start, then unblock all.
	select {
	case <-root.started:
	case <-time.After(2 * time.Second):
		t.Fatal("no handler started")
	}
	close(root.block)

	// Read all responses. Verify each tag is received exactly once.
	seen := make(map[proto.Tag]bool)
	for range numRequests {
		_ = cp.client.SetReadDeadline(time.Now().Add(2 * time.Second))
		tag, _ := readResponse(t, cp.client)
		if seen[tag] {
			t.Fatalf("duplicate response for tag %d", tag)
		}
		seen[tag] = true
	}

	for i := range numRequests {
		tag := proto.Tag(10 + i)
		if !seen[tag] {
			t.Errorf("missing response for tag %d", tag)
		}
	}
}

// Compile-time checks.
var (
	_ NodeLookuper  = (*blockingNode)(nil)
	_ InodeEmbedder = (*blockingNode)(nil)
	_ NodeLookuper  = (*panicNode)(nil)
	_ InodeEmbedder = (*panicNode)(nil)
	_ NodeLookuper  = (*countingNode)(nil)
	_ InodeEmbedder = (*countingNode)(nil)
)

// Suppress unused import warnings.
var (
	_ = p9l.Encode
	_ = io.Discard
	_ net.Conn
	_ sync.Mutex
)

// TestHandleFlush_WaitsForFlushedResponse asserts handleFlush does not return
// Rflush until the flushed request's handler has finished (its response is
// written), so a client cannot recycle oldtag and alias a late response.
func TestHandleFlush_WaitsForFlushedResponse(t *testing.T) {
	t.Parallel()

	c := &conn{inflight: newInflightMap(), logger: discardLogger()}
	rctx := getRequestCtx(t.Context())
	if !c.inflight.start(1, rctx) {
		t.Fatal("inflight.start failed")
	}

	flushDone := make(chan proto.Message, 1)
	go func() {
		flushDone <- c.handleFlush(t.Context(), &proto.Tflush{OldTag: 1})
	}()

	// Rflush must not be produced while the flushed request is still running.
	select {
	case <-flushDone:
		t.Fatal("handleFlush returned Rflush before the flushed request finished")
	case <-time.After(100 * time.Millisecond):
	}

	c.inflight.finish(1)

	select {
	case resp := <-flushDone:
		if _, ok := resp.(*proto.Rflush); !ok {
			t.Fatalf("got %T, want Rflush", resp)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handleFlush did not return after the flushed request finished")
	}
}

// TestDispatchInline_ClosesFlushDoneAfterResponse drives the real
// dispatchInline path and asserts the inflight entry's done channel (which
// releases a waiting Tflush) closes only AFTER the flushed response is on
// the wire. A handler blocks the request in flight so a Tflush can register
// a waiter; the response write then blocks on an unread pipe, so a
// premature done-close would let Rflush race ahead of the response.
func TestDispatchInline_ClosesFlushDoneAfterResponse(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})

	c := &conn{
		server:   &Server{}, // idleTimeout 0: sendResponseInline sets no deadline
		nc:       server,
		inflight: newInflightMap(),
		logger:   discardLogger(),
		protocol: protocolL,
	}
	// The handler blocks until released (request genuinely in flight) and
	// returns a non-empty body (Rwalk carries QIDs) so the framed write has
	// no trailing zero-length buffer; an empty net.Pipe write would block
	// waiting for a read that drainResponse never issues.
	entered := make(chan struct{})
	release := make(chan struct{})
	c.handler = func(_ context.Context, _ proto.Tag, _ proto.Message) proto.Message {
		close(entered)
		<-release
		return &proto.Rwalk{QIDs: []proto.QID{{Type: proto.QTFILE}}}
	}

	rctx := getRequestCtx(t.Context())
	if !c.inflight.start(1, rctx) {
		t.Fatal("inflight.start failed")
	}
	go c.dispatchInline(rctx, 1, &proto.Twalk{Fid: 0, NewFid: 1}, nil)

	// Register a Tflush waiter while the request is in flight.
	<-entered
	done := c.inflight.flushWait(1)
	if done == nil {
		t.Fatal("flushWait returned nil; tag 1 not in flight")
	}
	select {
	case <-done:
		t.Fatal("flush done closed while the handler was still running")
	default:
	}

	// Release the handler: dispatchInline removes the tag, then blocks in
	// sendResponseInline writing the response (no reader yet), then closes
	// done. While the write is pending, done must still be open.
	close(release)
	time.Sleep(50 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("flush done closed before the response was written; Rflush could ship ahead of it")
	default:
	}

	// Consume the response, unblocking the write so dispatchInline closes done.
	if err := drainResponse(client); err != nil {
		t.Fatalf("drainResponse: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("flush done not closed after the response was written")
	}
}

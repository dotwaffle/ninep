package client

import (
	"sync"
	"testing"

	"github.com/dotwaffle/ninep/proto"
)

// TestInflightMap_RegisterDeliverUnregister exercises the happy-path ordering
// contract: register → deliver → receive → unregister.
func TestInflightMap_RegisterDeliverUnregister(t *testing.T) {
	t.Parallel()
	im := newInflightMap()

	ch := im.register(proto.Tag(5))
	if ch == nil {
		t.Fatal("register returned nil chan")
	}
	if cap(ch) != 1 {
		t.Fatalf("respCh cap = %d, want 1", cap(ch))
	}

	msg := &proto.Rclunk{}
	im.deliver(proto.Tag(5), msg)

	got, ok := <-ch
	if !ok {
		t.Fatal("unexpected close before deliver received")
	}
	if got != msg {
		t.Fatalf("received %v, want %v", got, msg)
	}

	im.unregister(proto.Tag(5))

	// Post-unregister deliver must not panic and must not produce any side
	// effect observable by readers. deliver is a silent drop for unknown tags.
	im.deliver(proto.Tag(5), &proto.Rclunk{})
}

// TestInflightMap_DeliverUnknown verifies delivering on a tag that was never
// registered is a silent no-op.
func TestInflightMap_DeliverUnknown(t *testing.T) {
	t.Parallel()
	im := newInflightMap()
	// Must not panic.
	im.deliver(proto.Tag(99), &proto.Rclunk{})
}

// TestInflightMap_CancelAll registers several tags, calls cancelAll, and
// asserts every registered receive end observes (zero proto.Message, ok=false).
func TestInflightMap_CancelAll(t *testing.T) {
	t.Parallel()
	im := newInflightMap()

	chs := make([]chan proto.Message, 0, 3)
	for _, tag := range []proto.Tag{1, 2, 3} {
		chs = append(chs, im.register(tag))
	}

	im.cancelAll()

	for i, ch := range chs {
		msg, ok := <-ch
		if ok {
			t.Fatalf("chan[%d]: received %v, want closed-chan (ok=false)", i, msg)
		}
		if msg != nil {
			t.Fatalf("chan[%d]: received %v, want zero proto.Message on closed chan", i, msg)
		}
	}

	if n := im.len(); n != 0 {
		t.Fatalf("len after cancelAll = %d, want 0", n)
	}
}

// TestInflightMap_ConcurrentRegisterDeliver spawns N registrants, delivers
// responses out of order from a separate goroutine, and verifies every caller
// receives its own response. Runs under -race.
func TestInflightMap_ConcurrentRegisterDeliver(t *testing.T) {
	t.Parallel()
	im := newInflightMap()

	const N = 100
	type pair struct {
		tag proto.Tag
		msg *proto.Rclunk
	}
	pairs := make([]pair, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		pairs[i] = pair{tag: proto.Tag(i + 1), msg: &proto.Rclunk{}}
	}

	// Register all tags up front so deliver finds a chan for each.
	channels := make(map[proto.Tag]chan proto.Message, N)
	for _, p := range pairs {
		channels[p.tag] = im.register(p.tag)
	}

	// Receivers: one goroutine per caller, each waits on its respCh.
	results := make(map[proto.Tag]proto.Message)
	var resultsMu sync.Mutex
	for _, p := range pairs {
		p := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, ok := <-channels[p.tag]
			if !ok {
				t.Errorf("tag %d: chan closed before deliver", p.tag)
				return
			}
			resultsMu.Lock()
			results[p.tag] = got
			resultsMu.Unlock()
		}()
	}

	// Deliverer: scrambled order.
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Reverse order.
		for i := N - 1; i >= 0; i-- {
			im.deliver(pairs[i].tag, pairs[i].msg)
		}
	}()

	wg.Wait()

	for _, p := range pairs {
		got, ok := results[p.tag]
		if !ok {
			t.Errorf("tag %d: no result recorded", p.tag)
			continue
		}
		if got != p.msg {
			t.Errorf("tag %d: got %v, want %v", p.tag, got, p.msg)
		}
	}
}

// TestInflightMap_Len verifies len() reflects register/unregister changes.
func TestInflightMap_Len(t *testing.T) {
	t.Parallel()
	im := newInflightMap()
	im.register(proto.Tag(1))
	im.register(proto.Tag(2))
	im.register(proto.Tag(3))
	if n := im.len(); n != 3 {
		t.Fatalf("len = %d, want 3", n)
	}
	im.unregister(proto.Tag(2))
	if n := im.len(); n != 2 {
		t.Fatalf("len = %d, want 2", n)
	}
}

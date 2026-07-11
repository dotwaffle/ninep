package client_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

// blockingReadNode is a file whose Read parks until the request context is
// cancelled. It lets the test pin every client tag on a request that only
// a Tflush can release.
type blockingReadNode struct {
	server.Inode
	entered chan struct{} // one send per Read that has started blocking
}

func (n *blockingReadNode) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return nil, 0, nil
}

func (n *blockingReadNode) Read(ctx context.Context, _ []byte, _ uint64) (int, error) {
	select {
	case n.entered <- struct{}{}:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	<-ctx.Done()
	return 0, ctx.Err()
}

// TestFlushAndWait_SaturatedTagPoolCancellation is the regression test for
// the mass-cancellation deadlock: with every allocator tag held by an
// in-flight request, cancelling all of them at once made each
// flushAndWait block acquiring a Tflush tag from the exhausted pool --
// a tag only another blocked flushAndWait could free. Flush tags now come
// from the reserved mirror range (flushTagFor), so every cancelled caller
// must return promptly.
func TestFlushAndWait_SaturatedTagPoolCancellation(t *testing.T) {
	t.Parallel()

	const maxInflight = 4

	gen := &server.QIDGenerator{}
	root := &struct{ server.Inode }{}
	root.Init(gen.Next(proto.QTDIR), root)
	blocker := &blockingReadNode{entered: make(chan struct{}, maxInflight)}
	blocker.Init(gen.Next(proto.QTFILE), blocker)
	root.AddChild("block", blocker.EmbeddedInode())

	cli, cleanup := newClientServerPair(t, root,
		client.WithMaxInflight(maxInflight))
	defer cleanup()

	if _, err := cli.Raw().Tattach(t.Context(), proto.Fid(0), "me", "", proto.NoUID); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// Saturate: every tag ends up parked in a Read that only a flush can
	// release.
	ctx, cancel := context.WithCancel(t.Context())
	errs := make(chan error, maxInflight)
	var wg sync.WaitGroup
	for i := range maxInflight {
		fid := proto.Fid(100 + uint32(i))
		if _, err := cli.Raw().Twalk(t.Context(), proto.Fid(0), fid, []string{"block"}); err != nil {
			t.Fatalf("Walk fid %d: %v", fid, err)
		}
		if _, _, err := cli.Raw().Tlopen(t.Context(), fid, 0); err != nil {
			t.Fatalf("Open fid %d: %v", fid, err)
		}
		wg.Go(func() {
			_, err := cli.Raw().Tread(ctx, fid, 0, 16)
			errs <- err
		})
	}

	// Wait until all readers are parked server-side, then cancel them all
	// at once.
	for range maxInflight {
		select {
		case <-blocker.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("readers did not reach the blocking Read")
		}
	}
	cancel()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled requests did not return: flush-tag acquisition deadlocked on the saturated pool")
	}

	for range maxInflight {
		err := <-errs
		if err == nil {
			t.Error("cancelled Read returned nil error")
			continue
		}
		if !errors.Is(err, context.Canceled) && !errors.Is(err, client.ErrClosed) {
			t.Errorf("cancelled Read error = %v, want context.Canceled (or ErrClosed on the close race)", err)
		}
	}

	// The connection must still be usable: the tags all returned to the
	// pool and the mirror flush tags left the inflight map.
	if _, err := cli.Raw().Twalk(t.Context(), proto.Fid(0), proto.Fid(200), nil); err != nil {
		t.Fatalf("post-cancellation Walk: %v", err)
	}
}

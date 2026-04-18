package client

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// dialPairWriteT sets up a Conn with a mock Rversion server and returns
// the client Conn plus the server-side pipe. Like read_loop_internal_test
// helpers but with a configurable msize.
func dialPairWriteT(t *testing.T, msize uint32) (*Conn, net.Conn) {
	t.Helper()
	cliNC, srvNC := net.Pipe()
	t.Cleanup(func() { _ = srvNC.Close() })

	go func() {
		var sizeBuf [4]byte
		if _, err := io.ReadFull(srvNC, sizeBuf[:]); err != nil {
			return
		}
		size := binary.LittleEndian.Uint32(sizeBuf[:])
		body := make([]byte, int(size)-4)
		if _, err := io.ReadFull(srvNC, body); err != nil {
			return
		}
		_ = p9l.Encode(srvNC, proto.NoTag, &proto.Rversion{Msize: msize, Version: "9P2000.L"})
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	cli, err := Dial(ctx, cliNC, WithMsize(msize))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli, srvNC
}

// readOneFrame reads exactly one complete 9P frame from r, returning the
// full bytes (size[4] + type[1] + tag[2] + body).
func readOneFrame(t *testing.T, r net.Conn) []byte {
	t.Helper()
	var sizeBuf [4]byte
	if _, err := io.ReadFull(r, sizeBuf[:]); err != nil {
		t.Fatalf("read size: %v", err)
	}
	size := binary.LittleEndian.Uint32(sizeBuf[:])
	body := make([]byte, int(size)-4)
	if _, err := io.ReadFull(r, body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	out := make([]byte, 4+len(body))
	copy(out[:4], sizeBuf[:])
	copy(out[4:], body)
	return out
}

// TestWriteT_SingleRequest encodes a Twalk via writeT and asserts the
// server side reads the expected bytes.
func TestWriteT_SingleRequest(t *testing.T) {
	t.Parallel()
	cli, srvNC := dialPairWriteT(t, 65536)

	tw := &proto.Twalk{
		Fid:    proto.Fid(1),
		NewFid: proto.Fid(2),
		Names:  []string{"dir", "file"},
	}
	if err := cli.writeT(proto.Tag(7), tw); err != nil {
		t.Fatalf("writeT: %v", err)
	}

	frame := readOneFrame(t, srvNC)
	if len(frame) < 7 {
		t.Fatalf("frame too small: %d", len(frame))
	}
	gotType := proto.MessageType(frame[4])
	if gotType != proto.TypeTwalk {
		t.Errorf("type = %v, want Twalk", gotType)
	}
	gotTag := proto.Tag(binary.LittleEndian.Uint16(frame[5:7]))
	if gotTag != proto.Tag(7) {
		t.Errorf("tag = %d, want 7", gotTag)
	}

	// Decode the body and confirm fields match.
	var parsed proto.Twalk
	if err := parsed.DecodeFrom(bytes.NewReader(frame[7:])); err != nil {
		t.Fatalf("decode Twalk: %v", err)
	}
	if parsed.Fid != tw.Fid || parsed.NewFid != tw.NewFid {
		t.Errorf("decoded Twalk = %+v, want Fid/NewFid = %d/%d", parsed, tw.Fid, tw.NewFid)
	}
	if len(parsed.Names) != len(tw.Names) {
		t.Fatalf("decoded Names len = %d, want %d", len(parsed.Names), len(tw.Names))
	}
	for i, name := range tw.Names {
		if parsed.Names[i] != name {
			t.Errorf("Names[%d] = %q, want %q", i, parsed.Names[i], name)
		}
	}
}

// TestWriteT_ConcurrentCalls_NoInterleaving spawns 50 goroutines each
// calling writeT with a unique Twrite; reader goroutine consumes all 50
// frames and asserts each is complete (no interleaving).
func TestWriteT_ConcurrentCalls_NoInterleaving(t *testing.T) {
	t.Parallel()
	cli, srvNC := dialPairWriteT(t, 65536)

	const N = 50
	payload := bytes.Repeat([]byte("ABCDEFGH"), 16) // 128 bytes

	// Reader goroutine collects frames.
	framesCh := make(chan []byte, N)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for i := 0; i < N; i++ {
			var sizeBuf [4]byte
			if _, err := io.ReadFull(srvNC, sizeBuf[:]); err != nil {
				return
			}
			size := binary.LittleEndian.Uint32(sizeBuf[:])
			body := make([]byte, int(size)-4)
			if _, err := io.ReadFull(srvNC, body); err != nil {
				return
			}
			full := make([]byte, 4+len(body))
			copy(full[:4], sizeBuf[:])
			copy(full[4:], body)
			framesCh <- full
		}
	}()

	// 50 writer goroutines.
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			tw := &proto.Twrite{
				Fid:    proto.Fid(10 + i),
				Offset: uint64(i),
				Data:   payload,
			}
			if err := cli.writeT(proto.Tag(100+i), tw); err != nil {
				t.Errorf("writeT[%d]: %v", i, err)
			}
		}()
	}
	wg.Wait()

	// Wait for reader to collect all frames.
	select {
	case <-readerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("reader goroutine did not finish within 5s")
	}
	close(framesCh)

	// Validate each frame is complete + well-typed.
	count := 0
	for frame := range framesCh {
		count++
		if len(frame) < 7 {
			t.Errorf("short frame: %d bytes", len(frame))
			continue
		}
		gotType := proto.MessageType(frame[4])
		if gotType != proto.TypeTwrite {
			t.Errorf("frame type = %v, want Twrite", gotType)
		}
		// Decode the body — if interleaving happened, decode fails.
		var parsed proto.Twrite
		if err := parsed.DecodeFrom(bytes.NewReader(frame[7:])); err != nil {
			t.Errorf("decode Twrite: %v", err)
			continue
		}
		if !bytes.Equal(parsed.Data, payload) {
			t.Errorf("Twrite.Data mismatch: got len=%d, want len=%d", len(parsed.Data), len(payload))
		}
	}
	if count != N {
		t.Fatalf("read %d frames, want %d", count, N)
	}
}

// TestWriteT_AfterClose: after signalShutdown, writeT returns an error.
func TestWriteT_AfterClose(t *testing.T) {
	t.Parallel()
	cli, _ := dialPairWriteT(t, 65536)
	cli.signalShutdown()

	tw := &proto.Twalk{Fid: 1, NewFid: 2}
	err := cli.writeT(proto.Tag(7), tw)
	if err == nil {
		t.Fatal("writeT succeeded after signalShutdown, want error")
	}
}

// TestWriteT_NetBuffersReslice: call writeT 100 times in sequence;
// confirm every frame reaches the server side non-zero bytes (Pitfall 7
// — without re-slicing encBufsArr per call, subsequent writes ship 0
// bytes because net.Buffers.WriteTo's v.consume zeros both len and cap).
func TestWriteT_NetBuffersReslice(t *testing.T) {
	t.Parallel()
	cli, srvNC := dialPairWriteT(t, 65536)

	const N = 100
	// Reader collects each frame's size as the primary signal; Pitfall 7
	// would manifest as subsequent reads blocking (zero bytes written).
	sizes := make(chan uint32, N)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for i := 0; i < N; i++ {
			var sizeBuf [4]byte
			if _, err := io.ReadFull(srvNC, sizeBuf[:]); err != nil {
				return
			}
			size := binary.LittleEndian.Uint32(sizeBuf[:])
			body := make([]byte, int(size)-4)
			if _, err := io.ReadFull(srvNC, body); err != nil {
				return
			}
			sizes <- size
		}
	}()

	for i := 0; i < N; i++ {
		tw := &proto.Twalk{Fid: proto.Fid(i), NewFid: proto.Fid(i + N), Names: nil}
		if err := cli.writeT(proto.Tag(i+1), tw); err != nil {
			t.Fatalf("writeT[%d]: %v", i, err)
		}
	}

	select {
	case <-readerDone:
	case <-time.After(5 * time.Second):
		t.Fatalf("reader goroutine stuck; %d/%d frames received (Pitfall 7)", len(sizes), N)
	}

	close(sizes)
	count := 0
	for size := range sizes {
		if size == 0 {
			t.Errorf("frame size = 0 (Pitfall 7 — v.consume zeroed bufs)")
		}
		count++
	}
	if count != N {
		t.Fatalf("received %d frames, want %d", count, N)
	}
}

// TestWriteT_SizeExceedsMsize: writing a T-message whose encoded size >
// negotiated msize returns an error and does NOT ship bytes.
func TestWriteT_SizeExceedsMsize(t *testing.T) {
	t.Parallel()
	cli, _ := dialPairWriteT(t, 1024) // small msize

	// Twrite with a 2 KiB payload exceeds 1 KiB msize.
	tw := &proto.Twrite{
		Fid:    proto.Fid(1),
		Offset: 0,
		Data:   make([]byte, 2048),
	}
	err := cli.writeT(proto.Tag(1), tw)
	if err == nil {
		t.Fatal("writeT accepted oversize frame, want error")
	}
}

// TestWriteT_IsClosedHelper verifies isClosed returns false on a fresh
// Conn and true after signalShutdown.
func TestWriteT_IsClosedHelper(t *testing.T) {
	t.Parallel()
	cli, _ := dialPairWriteT(t, 65536)
	if cli.isClosed() {
		t.Fatal("isClosed = true on fresh Conn, want false")
	}
	cli.signalShutdown()
	if !cli.isClosed() {
		t.Fatal("isClosed = false after signalShutdown, want true")
	}
}

// Ensure errors import is used (some test paths may fall away under
// compiler optimization).
var _ = errors.Is

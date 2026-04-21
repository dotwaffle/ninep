package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

func TestTversion_Stress(t *testing.T) {
	// Baseline goroutine count for leak check.
	runtime.GC()
	baseline := runtime.NumGoroutine()

	root := &Inode{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	srv := New(root)

	const numConns = 100
	const iters = 10
	var wg sync.WaitGroup

	for i := range numConns {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			c1, c2 := net.Pipe()
			defer func() { _ = c1.Close() }()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go srv.ServeConn(ctx, c2)

			// Initial negotiation
			if err := stressWriteTversion(c1, proto.NoTag, 8192, "9P2000.L"); err != nil {
				return
			}
			if _, mtype, err := stressReadMsg(c1, nil); err != nil || mtype != proto.TypeRversion {
				return
			}

			// Iterative re-negotiation with small delay to respect 100ms limit
			for j := range iters {
				time.Sleep(110 * time.Millisecond)
				if err := stressWriteTversion(c1, proto.Tag(j+1), 8192, "9P2000.L"); err != nil {
					return
				}
				if _, mtype, err := stressReadMsg(c1, nil); err != nil || mtype != proto.TypeRversion {
					return
				}
			}
		}(i)
	}

	wg.Wait()

	// Wait for server goroutines to exit after pipe closures.
	time.Sleep(500 * time.Millisecond)
	runtime.GC()
	final := runtime.NumGoroutine()
	if final > baseline+15 { // Allow some slack for system goroutines
		t.Errorf("potential goroutine leak: baseline=%d, final=%d", baseline, final)
	}
}

func TestTversion_RateLimitStress(t *testing.T) {
	root := &Inode{}
	root.Init(proto.QID{Type: proto.QTDIR, Path: 1}, root)
	srv := New(root)

	c1, c2 := net.Pipe()
	defer func() { _ = c1.Close() }()
	ctx := t.Context()
	go srv.ServeConn(ctx, c2)

	// Initial negotiation
	_ = stressWriteTversion(c1, proto.NoTag, 8192, "9P2000.L")
	_, _, _ = stressReadMsg(c1, nil)

	// Hammer Tversion (should be rate-limited)
	const hammerCount = 20
	var receivedCount atomic.Int32

	go func() {
		for i := range hammerCount {
			_ = stressWriteTversion(c1, proto.Tag(i+1), 8192, "9P2000.L")
		}
	}()

	// Try to read responses with a timeout.
	// We expect only ONE or TWO responses if timing is tight.
	stop := time.After(500 * time.Millisecond)
loop:
	for {
		select {
		case <-stop:
			break loop
		default:
			_ = c1.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
			if _, _, err := stressReadMsg(c1, nil); err == nil {
				receivedCount.Add(1)
			} else {
				break loop
			}
		}
	}

	if receivedCount.Load() > 8 { // 500ms / 100ms = 5 + baseline + buffer
		t.Errorf("rate limiting failed: received %d responses for %d requests", receivedCount.Load(), hammerCount)
	}
}

func TestTversion_DrainTimeout(t *testing.T) {
	// We'll use a custom node that blocks Read for a long time.
	started := make(chan struct{})
	slowNode := &slowReadNode{started: started}
	slowNode.Init(proto.QID{Type: proto.QTFILE, Path: 2}, slowNode)

	srv := New(slowNode)

	c1, c2 := net.Pipe()
	defer func() { _ = c1.Close() }()
	ctx := t.Context()
	go srv.ServeConn(ctx, c2)

	// 1. Initial negotiation
	_ = stressWriteTversion(c1, proto.NoTag, 8192, "9P2000.L")
	_, _, _ = stressReadMsg(c1, nil)

	// 1b. Attach fid 0.
	ta := &proto.Tattach{Fid: 0, Afid: proto.NoFid, Uname: "me", Aname: ""}
	taBody := &bytes.Buffer{}
	_ = ta.EncodeTo(taBody)
	taHdr := make([]byte, proto.HeaderSize)
	binary.LittleEndian.PutUint32(taHdr[0:4], uint32(proto.HeaderSize)+uint32(taBody.Len()))
	taHdr[4] = uint8(proto.TypeTattach)
	binary.LittleEndian.PutUint16(taHdr[5:7], 50)
	_, _ = c1.Write(taHdr)
	_, _ = c1.Write(taBody.Bytes())
	_, mtype, err := stressReadMsg(c1, nil)
	if err != nil {
		t.Fatalf("read attach response: %v", err)
	}
	if mtype == proto.TypeRlerror {
		t.Fatal("attach failed with Rlerror")
	}

	// 1c. Open fid 0.
	to := &p9l.Tlopen{Fid: 0, Flags: uint32(syscall.O_RDONLY)}
	toBody := &bytes.Buffer{}
	_ = to.EncodeTo(toBody)
	toHdr := make([]byte, proto.HeaderSize)
	binary.LittleEndian.PutUint32(toHdr[0:4], uint32(proto.HeaderSize)+uint32(toBody.Len()))
	toHdr[4] = uint8(proto.TypeTlopen)
	binary.LittleEndian.PutUint16(toHdr[5:7], 60) // Tag 60
	_, _ = c1.Write(toHdr)
	_, _ = c1.Write(toBody.Bytes())
	_, mtype, err = stressReadMsg(c1, nil)
	if err != nil {
		t.Fatalf("read open response: %v", err)
	}
	if mtype == proto.TypeRlerror {
		t.Fatal("open failed with Rlerror")
	}

	// 2. Issue a slow Read.
	tr := &proto.Tread{Fid: 0, Offset: 0, Count: 10}
	trBody := &bytes.Buffer{}
	_ = tr.EncodeTo(trBody)
	trHdr := make([]byte, proto.HeaderSize)
	binary.LittleEndian.PutUint32(trHdr[0:4], uint32(proto.HeaderSize)+uint32(trBody.Len()))
	trHdr[4] = uint8(proto.TypeTread)
	binary.LittleEndian.PutUint16(trHdr[5:7], 100)
	_, _ = c1.Write(trHdr)
	_, _ = c1.Write(trBody.Bytes())

	// Wait for the handler to actually start.
	select {
	case <-started:
	case <-time.After(1 * time.Second):
		t.Fatal("handler never started")
	}

	// 3. Issue Tversion mid-flight.
	// Ensure we exceed the 100ms rate limit from the initial negotiation.
	time.Sleep(110 * time.Millisecond)
	start := time.Now()
	_ = stressWriteTversion(c1, 200, 8192, "9P2000.L") // Tag 200

	// Wait for Rversion. We might see the Rread/Rlerror for tag 100 first.
	for {
		tag, mtype, err := stressReadMsg(c1, nil)
		if err != nil {
			t.Fatalf("read msg: %v", err)
		}
		if tag == 200 {
			if mtype != proto.TypeRversion {
				t.Errorf("expected Rversion, got %v", mtype)
			}
			break
		}
	}
	elapsed := time.Since(start)

	// cleanupDeadline is 5s. If the drain timeout works, we should get the
	// response around 5s, not wait for the 10s sleep to finish.
	if elapsed < 4*time.Second {
		t.Errorf("Rversion arrived too early: %v", elapsed)
	}
	if elapsed > 8*time.Second {
		t.Errorf("Rversion arrived too late (drain timeout failed?): %v", elapsed)
	}
}

type slowReadNode struct {
	Inode
	started chan struct{}
}

func (n *slowReadNode) Open(ctx context.Context, flags uint32) (FileHandle, uint32, error) {
	return nil, 0, nil
}

func (n *slowReadNode) Read(ctx context.Context, buf []byte, offset uint64) (int, error) {
	if n.started != nil {
		close(n.started)
	}
	// Ignore ctx.Done() to force the server to wait for cleanupDeadline.
	time.Sleep(10 * time.Second)
	return 0, nil
}

// Helpers
func stressWriteTversion(c net.Conn, tag proto.Tag, msize uint32, version string) error {
	tv := &proto.Tversion{Msize: msize, Version: version}
	body := &bytes.Buffer{}
	_ = tv.EncodeTo(body)
	size := uint32(proto.HeaderSize) + uint32(body.Len())

	hdr := make([]byte, proto.HeaderSize)
	binary.LittleEndian.PutUint32(hdr[0:4], size)
	hdr[4] = uint8(proto.TypeTversion)
	binary.LittleEndian.PutUint16(hdr[5:7], uint16(tag))

	_, err := c.Write(hdr)
	if err != nil {
		return err
	}
	_, err = c.Write(body.Bytes())
	return err
}

func stressReadMsg(c net.Conn, msg proto.Message) (proto.Tag, proto.MessageType, error) {
	hdr := make([]byte, proto.HeaderSize)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return 0, 0, err
	}
	size := binary.LittleEndian.Uint32(hdr[0:4])
	mtype := proto.MessageType(hdr[4])
	tag := proto.Tag(binary.LittleEndian.Uint16(hdr[5:7]))

	bodySize := int64(size) - int64(proto.HeaderSize)
	if msg != nil && mtype == msg.Type() {
		err := msg.DecodeFrom(io.LimitReader(c, bodySize))
		return tag, mtype, err
	}

	// Skip body
	_, err := io.CopyN(io.Discard, c, bodySize)
	return tag, mtype, err
}

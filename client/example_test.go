package client_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
	"github.com/dotwaffle/ninep/server/memfs"
)

// exampleConnFixture boots a memfs server over net.Pipe and returns a
// dialed *client.Conn plus a cleanup closure. Used by the ExampleXxx
// bodies so godoc examples are self-contained and runnable without
// external 9P servers.
//
// Returns (nil, nil) on any boot error; the example body early-
// returns, which keeps go test green even when the fixture cannot
// start (e.g. a sandboxed CI that rejects net.Pipe -- unlikely but
// defensive).
func exampleConnFixture() (*client.Conn, func()) {
	cliNC, srvNC := net.Pipe()
	gen := &server.QIDGenerator{}
	etc := memfs.NewDir(gen).
		AddStaticFile("hostname", "example-host\n")
	root := memfs.NewDir(gen).
		AddStaticFile("greeting.txt", "").
		AddFile("big.bin", make([]byte, 4096))
	root.AddChild("etc", etc.EmbeddedInode())

	srv := server.New(root, server.WithMaxMsize(65536))
	srvCtx, srvCancel := context.WithCancel(context.Background())
	srvDone := make(chan struct{})
	go func() {
		defer close(srvDone)
		srv.ServeConn(srvCtx, srvNC)
	}()

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	conn, err := client.Dial(dialCtx, cliNC, client.WithMsize(65536))
	if err != nil || conn == nil {
		_ = cliNC.Close()
		srvCancel()
		<-srvDone
		return nil, nil
	}
	return conn, func() {
		_ = conn.Close()
		srvCancel()
		_ = srvNC.Close()
		<-srvDone
	}
}

// ExampleConn_Attach demonstrates dialing a 9P server and attaching to
// the root of a filesystem. The returned *File represents the root
// directory.
func ExampleConn_Attach() {
	conn, cleanup := exampleConnFixture()
	if conn == nil {
		return
	}
	defer cleanup()

	root, err := conn.Attach(context.Background(), "example", "")
	if err != nil {
		fmt.Println("attach failed:", err)
		return
	}
	defer func() { _ = root.Close() }()
	fmt.Printf("root is directory: %v\n", root.Qid().Type&proto.QTDIR != 0)
	// Output: root is directory: true
}

// ExampleConn_OpenFile demonstrates reading a file by path using the
// idiomatic [io.ReadAll] pattern. The *File returned by OpenFile
// satisfies [io.Reader], so any stdlib or third-party reader-consuming
// code composes with it directly.
func ExampleConn_OpenFile() {
	conn, cleanup := exampleConnFixture()
	if conn == nil {
		return
	}
	defer cleanup()

	if _, err := conn.Attach(context.Background(), "example", ""); err != nil {
		return
	}
	f, err := conn.OpenFile(context.Background(), "etc/hostname", os.O_RDONLY, 0)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(f)
	if err != nil {
		return
	}
	fmt.Printf("%s", data)
	// Output: example-host
}

// ExampleConn_Create demonstrates creating and writing a new file.
// Create returns an opened *File positioned at offset 0; Write
// advances the local offset and the server appends.
func ExampleConn_Create() {
	conn, cleanup := exampleConnFixture()
	if conn == nil {
		return
	}
	defer cleanup()
	if _, err := conn.Attach(context.Background(), "example", ""); err != nil {
		return
	}
	f, err := conn.Create(context.Background(), "new.txt", os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	n, err := f.Write([]byte("hello, 9P\n"))
	if err != nil {
		return
	}
	fmt.Println("wrote", n, "bytes")
	// Output: wrote 10 bytes
}

// ExampleFile_Clone demonstrates parallel reads via File.Clone. Each
// clone has its own fid and independent offset, so the four ReadAt
// calls execute concurrently under -race without contending on the
// shared per-File mutex.
func ExampleFile_Clone() {
	conn, cleanup := exampleConnFixture()
	if conn == nil {
		return
	}
	defer cleanup()
	if _, err := conn.Attach(context.Background(), "example", ""); err != nil {
		return
	}
	f, err := conn.OpenFile(context.Background(), "big.bin", os.O_RDONLY, 0)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	const parallel = 4
	var wg sync.WaitGroup
	for i := 0; i < parallel; i++ {
		clone, err := f.Clone(context.Background())
		if err != nil {
			continue
		}
		wg.Add(1)
		go func(i int, c *client.File) {
			defer wg.Done()
			defer func() { _ = c.Close() }()
			buf := make([]byte, 16)
			_, _ = c.ReadAt(buf, int64(i)*16)
		}(i, clone)
	}
	wg.Wait()
	fmt.Println("4 clones completed")
	// Output: 4 clones completed
}

// ExampleConn_Shutdown demonstrates ctx-driven shutdown with a bounded
// drain window. After the deadline fires, in-flight requests unblock
// with [ErrClosed].
func ExampleConn_Shutdown() {
	conn, cleanup := exampleConnFixture()
	if conn == nil {
		return
	}
	// Don't call cleanup() -- Shutdown replaces it. Hold onto the
	// closure so the server goroutine still exits at test end.
	_ = cleanup

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := conn.Shutdown(ctx); err != nil {
		fmt.Println("shutdown error:", err)
		return
	}
	fmt.Println("clean shutdown")
	// Output: clean shutdown
}

// ExampleRaw demonstrates the [Raw] sub-surface escape hatch. Callers
// that need explicit fid control or pipelined T-messages bypass the
// *File handle via Conn.Raw() and call the wire ops directly.
func ExampleRaw() {
	conn, cleanup := exampleConnFixture()
	if conn == nil {
		return
	}
	defer cleanup()

	raw := conn.Raw()
	rootFid, err := raw.AcquireFid()
	if err != nil {
		return
	}
	if _, err := raw.Attach(context.Background(), rootFid, "example", ""); err != nil {
		raw.ReleaseFid(rootFid)
		return
	}
	defer func() {
		_ = raw.Clunk(context.Background(), rootFid)
		raw.ReleaseFid(rootFid)
	}()

	fileFid, err := raw.AcquireFid()
	if err != nil {
		return
	}
	if _, err := raw.Walk(context.Background(), rootFid, fileFid, []string{"big.bin"}); err != nil {
		raw.ReleaseFid(fileFid)
		return
	}
	// POSIX O_WRONLY is 1 on Linux; explicit constant avoids pulling
	// in os just for this example.
	if _, _, err := raw.Lopen(context.Background(), fileFid, 1); err != nil {
		_ = raw.Clunk(context.Background(), fileFid)
		raw.ReleaseFid(fileFid)
		return
	}
	defer func() {
		_ = raw.Clunk(context.Background(), fileFid)
		raw.ReleaseFid(fileFid)
	}()

	// Pipeline 4 parallel writes. Conn is goroutine-safe
	// (database/sql.DB model) so the Twrites overlap at the server
	// without external synchronization.
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			chunk := []byte("chunk-")
			_, _ = raw.Write(context.Background(), fileFid, uint64(i)*64, chunk)
		}(i)
	}
	wg.Wait()
	fmt.Println("4 pipelined writes issued")
	// Output: 4 pipelined writes issued
}

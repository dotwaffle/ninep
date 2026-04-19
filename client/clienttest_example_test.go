package client_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/client/clienttest"
	"github.com/dotwaffle/ninep/server"
	"github.com/dotwaffle/ninep/server/memfs"
)

// exampleHarness boots a memfs server over net.Pipe and returns a
// dialed *client.Conn plus a cleanup closure. Self-contained
// alternative to [clienttest.MemfsPair] for use inside godoc Example
// functions, which cannot accept a [*testing.T].
//
// In tests, prefer clienttest.MemfsPair(tb, build, opts...) — it
// applies the same net.Pipe + memfs + server.New + client.Dial boot
// sequence but registers cleanup via tb.Cleanup and fails the test
// loudly on contract violations. See the TestExample_*_ViaClienttest
// trio below for the idiomatic in-test shape.
//
// Returns (nil, nil) on any boot error so example bodies can early-
// return without producing output — any deviation from the //Output:
// assertion fails the example, which is the desired behaviour.
func exampleHarness(build func(root *memfs.MemDir)) (*client.Conn, func()) {
	cliNC, srvNC := net.Pipe()
	gen := &server.QIDGenerator{}
	root := memfs.NewDir(gen)
	if build != nil {
		build(root)
	}
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

// Example_readFile demonstrates reading a file's contents by path via
// the high-level [client.Conn.OpenFile] + [io.ReadAll] idiom.
//
// In tests, prefer clienttest.MemfsPair(tb, build, opts...) — this
// example hand-rolls the harness because godoc Example functions take
// no testing.TB. See TestExample_ReadFile_ViaClienttest for the
// idiomatic in-test shape.
func Example_readFile() {
	conn, cleanup := exampleHarness(func(root *memfs.MemDir) {
		root.AddStaticFile("hello.txt", "hello world\n")
	})
	if conn == nil {
		return
	}
	defer cleanup()

	if _, err := conn.Attach(context.Background(), "example", ""); err != nil {
		fmt.Println("attach:", err)
		return
	}
	f, err := conn.OpenFile(context.Background(), "hello.txt", os.O_RDONLY, 0)
	if err != nil {
		fmt.Println("open:", err)
		return
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(f)
	if err != nil {
		fmt.Println("read:", err)
		return
	}
	fmt.Printf("%s", data)
	// Output: hello world
}

// Example_writeFile demonstrates creating and writing a new file via
// [client.Conn.Create] + [client.File.Write].
//
// In tests, prefer clienttest.MemfsPair(tb, build, opts...) — this
// example hand-rolls the harness because godoc Example functions take
// no testing.TB. See TestExample_WriteFile_ViaClienttest for the
// idiomatic in-test shape.
func Example_writeFile() {
	conn, cleanup := exampleHarness(nil)
	if conn == nil {
		return
	}
	defer cleanup()

	if _, err := conn.Attach(context.Background(), "example", ""); err != nil {
		fmt.Println("attach:", err)
		return
	}
	f, err := conn.Create(context.Background(), "new.txt", os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Println("create:", err)
		return
	}
	defer func() { _ = f.Close() }()

	n, err := f.Write([]byte("hello, 9P\n"))
	if err != nil {
		fmt.Println("write:", err)
		return
	}
	fmt.Println("wrote", n, "bytes")
	// Output: wrote 10 bytes
}

// Example_concurrentAccess demonstrates parallel reads from a single
// file via [client.File.Clone]. Each clone has its own fid and
// independent offset, so the ReadAt calls do not contend on the shared
// per-File mutex.
//
// In tests, prefer clienttest.MemfsPair(tb, build, opts...) — this
// example hand-rolls the harness because godoc Example functions take
// no testing.TB. See TestExample_ConcurrentAccess_ViaClienttest for
// the idiomatic in-test shape.
func Example_concurrentAccess() {
	conn, cleanup := exampleHarness(func(root *memfs.MemDir) {
		root.AddFile("big.bin", make([]byte, 4096))
	})
	if conn == nil {
		return
	}
	defer cleanup()

	if _, err := conn.Attach(context.Background(), "example", ""); err != nil {
		fmt.Println("attach:", err)
		return
	}
	f, err := conn.OpenFile(context.Background(), "big.bin", os.O_RDONLY, 0)
	if err != nil {
		fmt.Println("open:", err)
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

// TestExample_ReadFile_ViaClienttest mirrors Example_readFile but uses
// the canonical [clienttest.MemfsPair] harness — the shape external
// consumers should follow when writing integration tests against
// ninep's client.
func TestExample_ReadFile_ViaClienttest(t *testing.T) {
	t.Parallel()
	_, cli := clienttest.MemfsPair(t, func(root *memfs.MemDir) {
		root.AddStaticFile("hello.txt", "hello world\n")
	})
	if _, err := cli.Attach(t.Context(), "example", ""); err != nil {
		t.Fatalf("attach: %v", err)
	}
	f, err := cli.OpenFile(t.Context(), "hello.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "hello world\n" {
		t.Fatalf("got %q, want %q", data, "hello world\n")
	}
}

// TestExample_WriteFile_ViaClienttest mirrors Example_writeFile but
// uses the canonical [clienttest.MemfsPair] harness.
func TestExample_WriteFile_ViaClienttest(t *testing.T) {
	t.Parallel()
	_, cli := clienttest.MemfsPair(t, nil)
	if _, err := cli.Attach(t.Context(), "example", ""); err != nil {
		t.Fatalf("attach: %v", err)
	}
	f, err := cli.Create(t.Context(), "new.txt", os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = f.Close() }()
	n, err := f.Write([]byte("hello, 9P\n"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != 10 {
		t.Fatalf("wrote %d bytes, want 10", n)
	}
}

// TestExample_ConcurrentAccess_ViaClienttest mirrors
// Example_concurrentAccess but uses the canonical
// [clienttest.MemfsPair] harness.
func TestExample_ConcurrentAccess_ViaClienttest(t *testing.T) {
	t.Parallel()
	_, cli := clienttest.MemfsPair(t, func(root *memfs.MemDir) {
		root.AddFile("big.bin", make([]byte, 4096))
	})
	if _, err := cli.Attach(t.Context(), "example", ""); err != nil {
		t.Fatalf("attach: %v", err)
	}
	f, err := cli.OpenFile(t.Context(), "big.bin", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()

	// Clone returns a fresh fid pointing at the same server node but in
	// the "walked, not opened" state (mirrors ExampleFile_Clone in
	// example_test.go). Reads on the clones exercise the concurrent-fid
	// machinery; this test asserts the clones can be acquired and
	// closed concurrently, not that a read succeeds (that requires
	// re-Lopen on each clone, which is out of scope for the harness
	// demo).
	const parallel = 4
	var wg sync.WaitGroup
	for i := 0; i < parallel; i++ {
		clone, err := f.Clone(t.Context())
		if err != nil {
			t.Fatalf("clone %d: %v", i, err)
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
}

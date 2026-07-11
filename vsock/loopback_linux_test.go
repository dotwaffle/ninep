//go:build linux

package vsock_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/server"
	"github.com/dotwaffle/ninep/server/memfs"
	"github.com/dotwaffle/ninep/vsock"
	"golang.org/x/sys/unix"
)

// skipOrFatal skips when err indicates the vsock loopback transport is
// unavailable (module not loaded, AF_VSOCK unsupported, or the
// sandbox forbids it) and fails the test otherwise. On a CI runner with
// `modprobe vsock_loopback` loaded this branch is never taken, so the
// dedicated CI job asserts the tests are not skipped.
func skipOrFatal(t *testing.T, what string, err error) {
	t.Helper()
	switch {
	case errors.Is(err, unix.EAFNOSUPPORT),
		errors.Is(err, unix.EADDRNOTAVAIL),
		errors.Is(err, unix.ENODEV),
		errors.Is(err, unix.EPERM),
		errors.Is(err, unix.EACCES):
		t.Skipf("vsock loopback unavailable (%s: %v)", what, err)
	default:
		t.Fatalf("%s: %v", what, err)
	}
}

// listenLoopback returns a vsock listener bound to an explicit local port.
// Ephemeral (port 0) binds can be privileged (EACCES) on some kernels, so
// it probes a small range of high ports, skipping past ones already in use,
// and skips the whole test when the loopback transport is unavailable.
func listenLoopback(t *testing.T) (net.Listener, uint32) {
	t.Helper()
	const base = 0xC000 // 49152, high non-privileged range
	var lastErr error
	for p := uint32(base); p < base+64; p++ {
		ln, err := vsock.Listen(p)
		if err == nil {
			return ln, p
		}
		lastErr = err
		if errors.Is(err, unix.EADDRINUSE) {
			continue
		}
		skipOrFatal(t, "Listen", err) // skips on unavailable, fatals otherwise
	}
	skipOrFatal(t, "Listen", lastErr)
	return nil, 0 // unreachable: skipOrFatal exits the goroutine
}

// TestLoopbackRoundTrip exercises the transport in isolation: a byte echo
// over a Local (CID 1) connection.
func TestLoopbackRoundTrip(t *testing.T) {
	ln, port := listenLoopback(t)
	defer func() { _ = ln.Close() }()

	const msg = "ninep over vsock"
	var wg sync.WaitGroup
	wg.Go(func() {
		nc, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer func() { _ = nc.Close() }()
		buf := make([]byte, len(msg))
		if _, e := io.ReadFull(nc, buf); e != nil {
			return
		}
		_, _ = nc.Write(buf)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := vsock.Dial(ctx, vsock.Local, port)
	if err != nil {
		skipOrFatal(t, "Dial", err)
	}
	defer func() { _ = c.Close() }()

	if _, err := io.WriteString(c, msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != msg {
		t.Fatalf("echo mismatch: got %q want %q", got, msg)
	}
	wg.Wait()
}

// TestNinepOverVsock runs a real ninep memfs server and client over a Local
// (CID 1) vsock connection and reads a file end to end.
func TestNinepOverVsock(t *testing.T) {
	ln, port := listenLoopback(t)
	defer func() { _ = ln.Close() }()

	const content = "hello over vsock\n"
	gen := &server.QIDGenerator{}
	root := memfs.NewDir(gen)
	root.AddStaticFile("hello.txt", content)
	srv := server.New(root, server.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srvDone := make(chan struct{})
	go func() {
		defer close(srvDone)
		nc, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		srv.ServeConn(ctx, nc)
	}()

	dialCtx, dialCancel := context.WithTimeout(ctx, 5*time.Second)
	defer dialCancel()
	nc, err := vsock.Dial(dialCtx, vsock.Local, port)
	if err != nil {
		skipOrFatal(t, "Dial", err)
	}
	cli, err := client.Dial(dialCtx, nc)
	if err != nil {
		t.Fatalf("client.Dial: %v", err)
	}
	defer func() { _ = cli.Close() }()

	if _, err := cli.Attach(dialCtx, "tester", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	f, err := cli.OpenFile(dialCtx, "hello.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != content {
		t.Fatalf("content mismatch: got %q want %q", got, content)
	}

	cancel()
	<-srvDone
}

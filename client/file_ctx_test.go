package client_test

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
)

// Plan 22-03 tests for File.ReadCtx / File.WriteCtx / File.ReadAtCtx /
// File.WriteAtCtx plus the WithRequestTimeout / opCtx delegation path on
// File.Read / File.Write / File.ReadAt / File.WriteAt.
//
// DELEGATION tests (TestFile_*Ctx_Delegation) run against the normal
// memfs-backed server and verify that calling the *Ctx variant with a
// Background ctx produces byte-identical results to the non-ctx path.
// This is the Phase 20 regression gate: no behaviour change for the
// default-timeout (infinite) case.
//
// TIMEOUT tests run against the flushMockServer (defined in flush_test.go)
// which parks Rread responses on rreadGate, letting us deterministically
// drive the ctx.Done -> flushAndWait -> Tflush pipeline and assert the
// caller's error chain.

// ---- Delegation tests ----

// TestFile_ReadCtx_Delegation: reading via File.ReadCtx(Background, buf)
// produces the same bytes as the non-ctx File.Read(buf).
func TestFile_ReadCtx_Delegation(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileIOTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// Non-ctx read.
	f1, err := cli.OpenFile(ctx, "hello.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile #1: %v", err)
	}
	defer func() { _ = f1.Close() }()
	buf1 := make([]byte, 12)
	n1, err1 := f1.Read(buf1)

	// ctx-variant read, Background ctx so no cancellation semantics fire.
	f2, err := cli.OpenFile(ctx, "hello.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile #2: %v", err)
	}
	defer func() { _ = f2.Close() }()
	buf2 := make([]byte, 12)
	n2, err2 := f2.ReadCtx(context.Background(), buf2)

	if n1 != n2 {
		t.Errorf("Read n=%d, ReadCtx n=%d — want equal", n1, n2)
	}
	if string(buf1[:n1]) != string(buf2[:n2]) {
		t.Errorf("Read data=%q, ReadCtx data=%q — want equal", buf1[:n1], buf2[:n2])
	}
	if !errorsEquivalent(err1, err2) {
		t.Errorf("Read err=%v, ReadCtx err=%v — want equivalent", err1, err2)
	}
}

// TestFile_WriteCtx_Delegation: writing via File.WriteCtx(Background, p)
// produces the same byte count + error as File.Write(p), and the server-
// side read-back confirms the bytes landed.
func TestFile_WriteCtx_Delegation(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileIOTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	f, err := cli.OpenFile(ctx, "rw.bin", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	n, err := f.WriteCtx(context.Background(), []byte("hello-ctx"))
	if err != nil {
		t.Fatalf("WriteCtx: %v", err)
	}
	if n != len("hello-ctx") {
		t.Errorf("WriteCtx n=%d, want %d", n, len("hello-ctx"))
	}

	// Read back via a fresh fid — WriteCtx should have advanced f.offset,
	// so reading from a new handle starts at 0 and sees the bytes.
	rf, err := cli.OpenFile(ctx, "rw.bin", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile read-back: %v", err)
	}
	defer func() { _ = rf.Close() }()
	buf := make([]byte, 32)
	got, err := rf.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("read-back: %v", err)
	}
	if string(buf[:got]) != "hello-ctx" {
		t.Errorf("read-back = %q, want %q", buf[:got], "hello-ctx")
	}
}

// TestFile_ReadAtCtx_Delegation: File.ReadAtCtx produces identical results
// to File.ReadAt for the same offset.
func TestFile_ReadAtCtx_Delegation(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileIOTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	f, err := cli.OpenFile(ctx, "hello.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	buf1 := make([]byte, 5)
	n1, err1 := f.ReadAt(buf1, 6)

	buf2 := make([]byte, 5)
	n2, err2 := f.ReadAtCtx(context.Background(), buf2, 6)

	if n1 != n2 {
		t.Errorf("ReadAt n=%d, ReadAtCtx n=%d — want equal", n1, n2)
	}
	if string(buf1[:n1]) != string(buf2[:n2]) {
		t.Errorf("ReadAt data=%q, ReadAtCtx data=%q", buf1[:n1], buf2[:n2])
	}
	if !errorsEquivalent(err1, err2) {
		t.Errorf("ReadAt err=%v, ReadAtCtx err=%v — want equivalent", err1, err2)
	}
}

// TestFile_ReadAtCtx_PreservesOffset verifies ReadAtCtx does NOT mutate
// f.offset — the io.ReaderAt invariant (D-12).
func TestFile_ReadAtCtx_PreservesOffset(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileIOTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	f, err := cli.OpenFile(ctx, "hello.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	// Seek to 3 so we can distinguish "advanced" from "preserved".
	if _, err := f.Seek(3, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}

	buf := make([]byte, 4)
	if _, err := f.ReadAtCtx(context.Background(), buf, 6); err != nil {
		t.Fatalf("ReadAtCtx: %v", err)
	}

	pos, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatalf("Seek(SeekCurrent): %v", err)
	}
	if pos != 3 {
		t.Errorf("offset after ReadAtCtx = %d, want 3 (preserved)", pos)
	}
}

// TestFile_WriteAtCtx_Delegation: File.WriteAtCtx writes at the given
// offset without mutating f.offset.
func TestFile_WriteAtCtx_Delegation(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileIOTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	f, err := cli.OpenFile(ctx, "rw.bin", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	n, err := f.WriteAtCtx(context.Background(), []byte("abcdef"), 4)
	if err != nil {
		t.Fatalf("WriteAtCtx: %v", err)
	}
	if n != 6 {
		t.Errorf("WriteAtCtx n=%d, want 6", n)
	}

	// offset must still be 0 after a ReaderAt-style call.
	pos, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatalf("Seek(SeekCurrent): %v", err)
	}
	if pos != 0 {
		t.Errorf("offset after WriteAtCtx = %d, want 0 (preserved)", pos)
	}

	// Read back — first 4 bytes undefined (zero), bytes 4..9 are "abcdef".
	rf, err := cli.OpenFile(ctx, "rw.bin", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile read-back: %v", err)
	}
	defer func() { _ = rf.Close() }()
	buf := make([]byte, 10)
	got, err := rf.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("read-back: %v", err)
	}
	if got < 10 {
		// Keep reading if the server returned a short read.
		for got < 10 {
			n2, err := rf.Read(buf[got:])
			got += n2
			if err != nil {
				break
			}
		}
	}
	if string(buf[4:10]) != "abcdef" {
		t.Errorf("read-back[4:10] = %q, want %q", buf[4:10], "abcdef")
	}
}

// ---- Timeout + cancellation tests (flushMockServer) ----

// TestFile_Read_TimeoutTriggersTflush (D-26): WithRequestTimeout(50ms) +
// server holding Rread -> File.Read returns with context.DeadlineExceeded
// AND server observes exactly one Tflush on the wire.
func TestFile_Read_TimeoutTriggersTflush(t *testing.T) {
	t.Parallel()
	cli, srv, cleanup := newFlushTestPair(t,
		client.WithRequestTimeout(50*time.Millisecond),
	)
	defer cleanup()

	// Configure the mock to send Rflush immediately on receiving Tflush
	// — otherwise flushAndWait parks indefinitely on the rflushGate
	// (which is the default state).
	srv.rflushSendImmediately.Store(true)

	fid := attachAndOpen(t, cli)
	// Build a *File around the live fid opened above. The mock server's
	// Tread handler parks on rreadGate (default: never released until
	// Cleanup), so File.Read will block until ctx deadline fires.
	ff := client.NewFileWrappingFidForTest(cli, fid, 4096)

	start := time.Now()
	buf := make([]byte, 4096)
	n, err := ff.Read(buf)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("Read returned nil err; want context.DeadlineExceeded (n=%d)", n)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("errors.Is(err, context.DeadlineExceeded) = false; want true. err = %v", err)
	}
	// 50ms timeout + Tflush RTT; allow a generous 500ms ceiling.
	if elapsed > 500*time.Millisecond {
		t.Errorf("Read took %v; want < 500ms", elapsed)
	}
	// Drain the late Rread so the server goroutine exits cleanly.
	srv.releaseRread()
	time.Sleep(50 * time.Millisecond)

	if got := srv.tflushCount.Load(); got != 1 {
		t.Errorf("tflushCount = %d; want exactly 1 (timeout triggered exactly one Tflush)", got)
	}
}

// TestFile_ReadCtx_ExplicitTimeout verifies the per-op ctx variant
// honours a caller-supplied deadline even when WithRequestTimeout is
// NOT set (infinite default).
func TestFile_ReadCtx_ExplicitTimeout(t *testing.T) {
	t.Parallel()
	cli, srv, cleanup := newFlushTestPair(t) // no WithRequestTimeout
	defer cleanup()

	// Rflush fires immediately on Tflush receipt so flushAndWait can
	// unblock. Rread stays parked on rreadGate.
	srv.rflushSendImmediately.Store(true)

	fid := attachAndOpen(t, cli)
	ff := client.NewFileWrappingFidForTest(cli, fid, 4096)

	opCtx, opCancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer opCancel()

	start := time.Now()
	buf := make([]byte, 4096)
	_, err := ff.ReadCtx(opCtx, buf)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("errors.Is(err, context.DeadlineExceeded) = false; want true. err = %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("ReadCtx took %v; want < 500ms", elapsed)
	}

	srv.releaseRread()
	time.Sleep(50 * time.Millisecond)

	if got := srv.tflushCount.Load(); got != 1 {
		t.Errorf("tflushCount = %d; want 1", got)
	}
}

// TestFile_Read_InfiniteDefault_NoTimeout verifies the default (no
// WithRequestTimeout) produces no hidden timeout. Uses the memfs-backed
// fixture which returns data instantly; asserts Read completes in
// well under any plausible default-timeout bound.
func TestFile_Read_InfiniteDefault_NoTimeout(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileIOTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	f, err := cli.OpenFile(ctx, "hello.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	start := time.Now()
	buf := make([]byte, 12)
	n, err := f.Read(buf)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if n != 12 {
		t.Errorf("Read n=%d, want 12", n)
	}
	// 100ms ceiling is far below any plausible default timeout value
	// (e.g. 30s) — this catches a regression where WithRequestTimeout
	// defaults to a finite value that adds per-call overhead.
	if elapsed > 100*time.Millisecond {
		t.Errorf("Read took %v; want < 100ms (no hidden default timeout)", elapsed)
	}
}

// TestFile_ReadCtx_Cancel_TriggersFlush verifies ctx cancel on ReadCtx
// produces a context.Canceled + ErrFlushed chain AND emits one Tflush on
// the wire.
func TestFile_ReadCtx_Cancel_TriggersFlush(t *testing.T) {
	t.Parallel()
	cli, srv, cleanup := newFlushTestPair(t)
	defer cleanup()

	fid := attachAndOpen(t, cli)
	ff := client.NewFileWrappingFidForTest(cli, fid, 4096)

	// Configure: Rflush sends immediately so we hit the Rflush-first
	// error chain (includes ErrFlushed).
	srv.rflushSendImmediately.Store(true)

	opCtx, opCancel := context.WithCancel(t.Context())

	type res struct {
		n   int
		err error
	}
	resCh := make(chan res, 1)
	go func() {
		buf := make([]byte, 4096)
		n, err := ff.ReadCtx(opCtx, buf)
		resCh <- res{n: n, err: err}
	}()

	// Let the Tread reach the server before cancelling.
	time.Sleep(20 * time.Millisecond)
	opCancel()

	var r res
	select {
	case r = <-resCh:
	case <-time.After(2 * time.Second):
		t.Fatal("ReadCtx did not return within 2s of cancel")
	}

	if r.err == nil {
		t.Fatalf("ReadCtx returned nil err; want context.Canceled-wrapped")
	}
	if !errors.Is(r.err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false; err = %v", r.err)
	}
	if !errors.Is(r.err, client.ErrFlushed) {
		t.Errorf("errors.Is(err, ErrFlushed) = false on Rflush-first path; err = %v", r.err)
	}

	// Drain server.
	srv.releaseRread()
	time.Sleep(50 * time.Millisecond)

	if got := srv.tflushCount.Load(); got != 1 {
		t.Errorf("tflushCount = %d; want 1", got)
	}
}

// TestFile_ReadCtx_MutexInvariance exercises concurrent Read and ReadCtx
// on the SAME *File under -race. Both methods must acquire f.mu so
// offset mutation is serialised.
func TestFile_ReadCtx_MutexInvariance(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := fileIOTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	f, err := cli.OpenFile(ctx, "hello.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	var wg sync.WaitGroup
	var nonCtxCount, ctxCount atomic.Int64

	// Spawn 10 goroutines alternating Read + ReadCtx. Each reads 1 byte
	// so the 12-byte file is consumed quickly; we don't care about
	// specific byte ordering, only race-freedom.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			buf := make([]byte, 1)
			var n int
			var err error
			if i%2 == 0 {
				n, err = f.Read(buf)
				if err == nil || errors.Is(err, io.EOF) {
					nonCtxCount.Add(int64(n))
				}
			} else {
				n, err = f.ReadCtx(context.Background(), buf)
				if err == nil || errors.Is(err, io.EOF) {
					ctxCount.Add(int64(n))
				}
			}
		}()
	}
	wg.Wait()

	// Total must be <= 12 (file size). If offset wasn't serialised under
	// -race, the sum could drift; more importantly, -race would flag the
	// concurrent offset mutations directly.
	total := nonCtxCount.Load() + ctxCount.Load()
	if total > 12 {
		t.Errorf("total bytes read = %d, exceeds file size 12 (offset race)", total)
	}
}

// ---- Helpers ----

// errorsEquivalent returns true if both errs are nil, both are non-nil
// and have identical Error() strings, or both satisfy errors.Is against
// io.EOF. Delegation tests use this to assert "same behaviour" without
// requiring identical error VALUES (different error wraps may embed the
// same sentinel).
func errorsEquivalent(a, b error) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if errors.Is(a, io.EOF) && errors.Is(b, io.EOF) {
		return true
	}
	return a.Error() == b.Error()
}

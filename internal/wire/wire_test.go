package wire_test

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"testing"

	"github.com/dotwaffle/ninep/internal/wire"
	"github.com/dotwaffle/ninep/proto"
)

func TestReadSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   []byte
		want    uint32
		wantErr bool
		errIs   error
	}{
		{
			name:  "happy: size=16 fits encoded frame (header+body)",
			input: []byte{0x10, 0x00, 0x00, 0x00},
			want:  16,
		},
		{
			name:  "happy: size == HeaderSize exactly (7)",
			input: []byte{0x07, 0x00, 0x00, 0x00},
			want:  7,
		},
		{
			name:    "too small: size=0 < HeaderSize",
			input:   []byte{0x00, 0x00, 0x00, 0x00},
			wantErr: true,
		},
		{
			name:    "too small: size=6 < HeaderSize",
			input:   []byte{0x06, 0x00, 0x00, 0x00},
			wantErr: true,
		},
		{
			name:    "truncated: zero bytes => EOF",
			input:   []byte{},
			wantErr: true,
			errIs:   io.EOF,
		},
		{
			name:    "truncated: one byte => ErrUnexpectedEOF",
			input:   []byte{0x10},
			wantErr: true,
			errIs:   io.ErrUnexpectedEOF,
		},
		{
			name:    "truncated: two bytes => ErrUnexpectedEOF",
			input:   []byte{0x10, 0x00},
			wantErr: true,
			errIs:   io.ErrUnexpectedEOF,
		},
		{
			name:    "truncated: three bytes => ErrUnexpectedEOF",
			input:   []byte{0x10, 0x00, 0x00},
			wantErr: true,
			errIs:   io.ErrUnexpectedEOF,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := wire.ReadSize(bytes.NewReader(tc.input))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ReadSize(%v): want error, got nil (result=%d)", tc.input, got)
				}
				if tc.errIs != nil && !errors.Is(err, tc.errIs) {
					t.Fatalf("ReadSize(%v): errors.Is not satisfied: got %v, want %v", tc.input, err, tc.errIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadSize(%v): unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("ReadSize(%v): got %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

func TestReadBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   []byte
		bufLen  int
		wantErr bool
		errIs   error
	}{
		{
			name:   "happy: 32 bytes into 32-byte buf",
			input:  make([]byte, 32),
			bufLen: 32,
		},
		{
			name:   "zero-length body: bufLen=0 with empty reader returns nil",
			input:  []byte{},
			bufLen: 0,
		},
		{
			name:    "truncated: 10-byte reader into 32-byte buf",
			input:   make([]byte, 10),
			bufLen:  32,
			wantErr: true,
			errIs:   io.ErrUnexpectedEOF,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			buf := make([]byte, tc.bufLen)
			err := wire.ReadBody(bytes.NewReader(tc.input), buf)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ReadBody: want error, got nil")
				}
				if tc.errIs != nil && !errors.Is(err, tc.errIs) {
					t.Fatalf("ReadBody: errors.Is not satisfied: got %v, want %v", err, tc.errIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadBody: unexpected error: %v", err)
			}
		})
	}
}

// TestReadBody_PreservesSliceLenCap locks the bucket-pool invariant. ReadBody
// must NOT resize the slice it receives — callers (server/conn.go) source buf
// from bufpool.GetMsgBuf which returns a bucket-sized buffer sliced to the
// requested length. If ReadBody resized via append, PutMsgBuf would fail to
// match the bucket cap and drop the buffer to GC.
func TestReadBody_PreservesSliceLenCap(t *testing.T) {
	t.Parallel()

	// Simulate the bufpool.GetMsgBuf(32) pattern: underlying 1 KiB bucket,
	// sliced to the requested body length.
	backing := make([]byte, 0, 1024)
	buf := backing[:32]

	if err := wire.ReadBody(bytes.NewReader(make([]byte, 32)), buf); err != nil {
		t.Fatalf("ReadBody: %v", err)
	}
	if got := len(buf); got != 32 {
		t.Fatalf("len(buf) changed: got %d, want 32", got)
	}
	if got := cap(buf); got != 1024 {
		t.Fatalf("cap(buf) changed: got %d, want 1024", got)
	}
}

// TestReadSize_ZeroAlloc asserts that ReadSize does not allocate on the hot
// path. The stack-local hdr [4]byte and the passed-in reader must not escape.
// We use a hoisted *bytes.Reader with Reset to exclude the reader's own cost.
func TestReadSize_ZeroAlloc(t *testing.T) {
	hdrBytes := []byte{0x10, 0x00, 0x00, 0x00}
	r := bytes.NewReader(hdrBytes)

	allocs := testing.AllocsPerRun(100, func() {
		r.Reset(hdrBytes)
		if _, err := wire.ReadSize(r); err != nil {
			t.Fatalf("ReadSize: %v", err)
		}
	})
	if allocs != 0 {
		t.Fatalf("ReadSize allocs/op = %v, want 0", allocs)
	}
}

// TestReadBody_ZeroAlloc asserts ReadBody allocates nothing when the caller
// provides a reusable reader and buffer. io.ReadFull on a *bytes.Reader is
// zero-alloc for small reads.
func TestReadBody_ZeroAlloc(t *testing.T) {
	body := make([]byte, 64)
	buf := make([]byte, 64)
	r := bytes.NewReader(body)

	allocs := testing.AllocsPerRun(100, func() {
		r.Reset(body)
		if err := wire.ReadBody(r, buf); err != nil {
			t.Fatalf("ReadBody: %v", err)
		}
	})
	if allocs != 0 {
		t.Fatalf("ReadBody allocs/op = %v, want 0", allocs)
	}
}

// TestWriteFramesLocked_Smoke exercises the happy path over net.Pipe. The
// net.Pipe fallback triggers sequential Writes (no writev), which is the
// identical wire-level semantic.
func TestWriteFramesLocked_Smoke(t *testing.T) {
	t.Parallel()

	srv, cli := net.Pipe()
	t.Cleanup(func() { _ = srv.Close(); _ = cli.Close() })

	parts := [][]byte{
		[]byte("AAA"),
		[]byte("BBB"),
		[]byte("CCC"),
	}
	want := []byte("AAABBBCCC")

	errCh := make(chan error, 1)
	go func() {
		bufs := net.Buffers(parts)
		errCh <- wire.WriteFramesLocked(srv, &bufs)
		_ = srv.Close()
	}()

	got, err := io.ReadAll(cli)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("read bytes: got %q, want %q", got, want)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("WriteFramesLocked: %v", err)
	}
}

// TestWriteFramesLocked_ConsumesBufs locks the documented v.consume semantic:
// after a successful call, *bufs has both length AND capacity zeroed.
// This test is the executable contract for the godoc's warning.
func TestWriteFramesLocked_ConsumesBufs(t *testing.T) {
	t.Parallel()

	srv, cli := net.Pipe()
	t.Cleanup(func() { _ = srv.Close(); _ = cli.Close() })

	// Drain the reader concurrently so WriteFramesLocked can complete.
	readDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, cli)
		close(readDone)
	}()

	parts := [][]byte{
		[]byte("AA"),
		[]byte("BB"),
		[]byte("CC"),
	}
	bufs := net.Buffers(parts)
	if pre := len(bufs); pre != 3 {
		t.Fatalf("pre-call len(bufs) = %d, want 3", pre)
	}

	if err := wire.WriteFramesLocked(srv, &bufs); err != nil {
		t.Fatalf("WriteFramesLocked: %v", err)
	}

	// v.consume zeroes both length AND capacity on full consumption. This is
	// the footgun documented on WriteFramesLocked — callers must re-slice
	// from a conn-resident backing array on every call.
	if got := len(bufs); got != 0 {
		t.Fatalf("post-call len(bufs) = %d, want 0 (v.consume semantic)", got)
	}
	if got := cap(bufs); got != 0 {
		t.Fatalf("post-call cap(bufs) = %d, want 0 (v.consume semantic)", got)
	}

	_ = srv.Close()
	<-readDone
}

// TestWriteFramesLocked_LockingIsCallersJob demonstrates the caller-contract
// side of the *Locked naming: when callers hold a shared mutex around every
// WriteFramesLocked call on the same connection, concurrent writes do not
// interleave. Go has no lock-ownership type system, so this test documents
// correct usage rather than detecting misuse.
func TestWriteFramesLocked_LockingIsCallersJob(t *testing.T) {
	t.Parallel()

	srv, cli := net.Pipe()
	t.Cleanup(func() { _ = srv.Close(); _ = cli.Close() })

	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(2)

	// Each goroutine writes a distinct 4-byte marker while holding mu. If
	// the mutex contract is honoured, the reader sees exactly one complete
	// 4-byte marker followed by another — never interleaved halves.
	writeMsg := func(payload []byte) {
		defer wg.Done()
		mu.Lock()
		defer mu.Unlock()
		parts := [][]byte{payload}
		bufs := net.Buffers(parts)
		if err := wire.WriteFramesLocked(srv, &bufs); err != nil {
			t.Errorf("WriteFramesLocked: %v", err)
		}
	}

	msgA := []byte("AAAA")
	msgB := []byte("BBBB")
	go writeMsg(msgA)
	go writeMsg(msgB)

	// Read 8 bytes: two 4-byte markers back-to-back.
	got := make([]byte, 8)
	if _, err := io.ReadFull(cli, got); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	wg.Wait()

	// The two 4-byte halves must each equal one of the markers and the two
	// halves must differ — interleaving would produce mixed bytes.
	first := got[:4]
	second := got[4:]
	okFirst := bytes.Equal(first, msgA) || bytes.Equal(first, msgB)
	okSecond := bytes.Equal(second, msgA) || bytes.Equal(second, msgB)
	if !okFirst || !okSecond {
		t.Fatalf("interleaved output: got %q", got)
	}
	if bytes.Equal(first, second) {
		t.Fatalf("same message twice, contract violated: got %q", got)
	}
}

// Reference the proto package so the import stays used even if future test
// tweaks drop direct references. HeaderSize is the validation threshold that
// ReadSize enforces; asserting its value here pins the wire-level contract.
func TestHeaderSizeUnchanged(t *testing.T) {
	t.Parallel()
	if proto.HeaderSize != 7 {
		t.Fatalf("proto.HeaderSize = %d, want 7 (wire.ReadSize depends on this value)", proto.HeaderSize)
	}
}

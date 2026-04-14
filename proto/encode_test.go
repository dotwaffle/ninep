package proto

import (
	"bytes"
	"strings"
	"testing"
)

// The following tests pin the *bytes.Buffer fast-path allocs to 0. They
// exist as a regression guard: if a future change causes the Write*
// helpers to escape their scratch buffer to the heap (e.g. by losing the
// concrete-type fast path), these tests fail.
//
// testing.AllocsPerRun cannot run inside a t.Parallel() test, so these
// are serialized. They are cheap (1000 iterations of a single helper).

func TestWriteUint8_BufferFastPath_ZeroAllocs(t *testing.T) {
	buf := bytes.NewBuffer(make([]byte, 0, 1024))
	for range 10 {
		buf.Reset()
		_ = WriteUint8(buf, 0x42)
	}
	allocs := testing.AllocsPerRun(1000, func() {
		buf.Reset()
		_ = WriteUint8(buf, 0x42)
	})
	if allocs != 0 {
		t.Errorf("WriteUint8 fast-path allocs/op: got %v, want 0", allocs)
	}
}

func TestWriteUint16_BufferFastPath_ZeroAllocs(t *testing.T) {
	buf := bytes.NewBuffer(make([]byte, 0, 1024))
	for range 10 {
		buf.Reset()
		_ = WriteUint16(buf, 0x1234)
	}
	allocs := testing.AllocsPerRun(1000, func() {
		buf.Reset()
		_ = WriteUint16(buf, 0x1234)
	})
	if allocs != 0 {
		t.Errorf("WriteUint16 fast-path allocs/op: got %v, want 0", allocs)
	}
}

func TestWriteUint32_BufferFastPath_ZeroAllocs(t *testing.T) {
	buf := bytes.NewBuffer(make([]byte, 0, 1024))
	for range 10 {
		buf.Reset()
		_ = WriteUint32(buf, 0xDEADBEEF)
	}
	allocs := testing.AllocsPerRun(1000, func() {
		buf.Reset()
		_ = WriteUint32(buf, 0xDEADBEEF)
	})
	if allocs != 0 {
		t.Errorf("WriteUint32 fast-path allocs/op: got %v, want 0", allocs)
	}
}

func TestWriteUint64_BufferFastPath_ZeroAllocs(t *testing.T) {
	buf := bytes.NewBuffer(make([]byte, 0, 1024))
	for range 10 {
		buf.Reset()
		_ = WriteUint64(buf, 0xDEADBEEFCAFEBABE)
	}
	allocs := testing.AllocsPerRun(1000, func() {
		buf.Reset()
		_ = WriteUint64(buf, 0xDEADBEEFCAFEBABE)
	})
	if allocs != 0 {
		t.Errorf("WriteUint64 fast-path allocs/op: got %v, want 0", allocs)
	}
}

func TestWriteString_BufferFastPath_ZeroAllocs(t *testing.T) {
	// Use a short string so length-prefix + body fit in AvailableBuffer
	// without triggering a grow.
	s := "9P2000.L"
	buf := bytes.NewBuffer(make([]byte, 0, 1024))
	for range 10 {
		buf.Reset()
		_ = WriteString(buf, s)
	}
	allocs := testing.AllocsPerRun(1000, func() {
		buf.Reset()
		_ = WriteString(buf, s)
	})
	if allocs != 0 {
		t.Errorf("WriteString fast-path allocs/op: got %v, want 0", allocs)
	}
}

func TestWriteString_BufferFastPath_Empty_ZeroAllocs(t *testing.T) {
	buf := bytes.NewBuffer(make([]byte, 0, 1024))
	for range 10 {
		buf.Reset()
		_ = WriteString(buf, "")
	}
	allocs := testing.AllocsPerRun(1000, func() {
		buf.Reset()
		_ = WriteString(buf, "")
	})
	if allocs != 0 {
		t.Errorf("WriteString (empty) fast-path allocs/op: got %v, want 0", allocs)
	}
}

func TestWriteQID_BufferFastPath_ZeroAllocs(t *testing.T) {
	q := QID{Type: QTDIR, Version: 42, Path: 0xDEADBEEF01234567}
	buf := bytes.NewBuffer(make([]byte, 0, 1024))
	for range 10 {
		buf.Reset()
		_ = WriteQID(buf, q)
	}
	allocs := testing.AllocsPerRun(1000, func() {
		buf.Reset()
		_ = WriteQID(buf, q)
	})
	if allocs != 0 {
		t.Errorf("WriteQID fast-path allocs/op: got %v, want 0", allocs)
	}
}

// TestWriteString_MaxStringLen_BeforeTypeAssertion verifies that the
// MaxStringLen length bound is enforced on both the fast path (concrete
// *bytes.Buffer) and the slow path (io.Writer interface). The validation
// must run BEFORE the type assertion, not inside either branch.
func TestWriteString_MaxStringLen_BeforeTypeAssertion(t *testing.T) {
	t.Parallel()
	oversize := strings.Repeat("x", MaxStringLen+1)

	// Fast path (*bytes.Buffer): must error.
	buf := bytes.NewBuffer(make([]byte, 0, 1024))
	if err := WriteString(buf, oversize); err == nil {
		t.Error("WriteString(*bytes.Buffer, oversize) returned nil error, want non-nil")
	}
	if buf.Len() != 0 {
		t.Errorf("WriteString(*bytes.Buffer, oversize) wrote %d bytes on error; want 0", buf.Len())
	}

	// Slow path: wrap the buffer in an io.Writer adapter so the type
	// assertion to *bytes.Buffer fails and the fallback path runs.
	slowInner := bytes.NewBuffer(make([]byte, 0, 1024))
	slow := writerAdapter{w: slowInner}
	if err := WriteString(slow, oversize); err == nil {
		t.Error("WriteString(io.Writer, oversize) returned nil error, want non-nil")
	}
	if slowInner.Len() != 0 {
		t.Errorf("WriteString(io.Writer, oversize) wrote %d bytes on error; want 0", slowInner.Len())
	}
}

// TestWriteHelpers_FallbackPath_WireFormat verifies that the slow path
// (io.Writer non-*bytes.Buffer) produces bit-identical output to the
// fast path. We route through an adapter that satisfies io.Writer but
// is NOT *bytes.Buffer, forcing the fallback branch.
func TestWriteHelpers_FallbackPath_WireFormat(t *testing.T) {
	t.Parallel()

	// fastBuf uses the concrete *bytes.Buffer path.
	fastBuf := &bytes.Buffer{}
	// slow is wrapped so w.(*bytes.Buffer) assertion fails; it uses the
	// io.Writer fallback path.
	slowInner := &bytes.Buffer{}
	slow := writerAdapter{w: slowInner}

	if err := WriteUint8(fastBuf, 0x42); err != nil {
		t.Fatalf("fast WriteUint8: %v", err)
	}
	if err := WriteUint8(slow, 0x42); err != nil {
		t.Fatalf("slow WriteUint8: %v", err)
	}
	if err := WriteUint16(fastBuf, 0x1234); err != nil {
		t.Fatalf("fast WriteUint16: %v", err)
	}
	if err := WriteUint16(slow, 0x1234); err != nil {
		t.Fatalf("slow WriteUint16: %v", err)
	}
	if err := WriteUint32(fastBuf, 0xDEADBEEF); err != nil {
		t.Fatalf("fast WriteUint32: %v", err)
	}
	if err := WriteUint32(slow, 0xDEADBEEF); err != nil {
		t.Fatalf("slow WriteUint32: %v", err)
	}
	if err := WriteUint64(fastBuf, 0xCAFEBABEDEADBEEF); err != nil {
		t.Fatalf("fast WriteUint64: %v", err)
	}
	if err := WriteUint64(slow, 0xCAFEBABEDEADBEEF); err != nil {
		t.Fatalf("slow WriteUint64: %v", err)
	}
	if err := WriteString(fastBuf, "hello"); err != nil {
		t.Fatalf("fast WriteString: %v", err)
	}
	if err := WriteString(slow, "hello"); err != nil {
		t.Fatalf("slow WriteString: %v", err)
	}
	q := QID{Type: QTDIR, Version: 42, Path: 0xDEADBEEF01234567}
	if err := WriteQID(fastBuf, q); err != nil {
		t.Fatalf("fast WriteQID: %v", err)
	}
	if err := WriteQID(slow, q); err != nil {
		t.Fatalf("slow WriteQID: %v", err)
	}

	if !bytes.Equal(fastBuf.Bytes(), slowInner.Bytes()) {
		t.Errorf("fast path and slow path produced different bytes:\nfast: %x\nslow: %x",
			fastBuf.Bytes(), slowInner.Bytes())
	}
}

// writerAdapter wraps a bytes.Buffer so that type assertion to
// *bytes.Buffer on the io.Writer fails, forcing the fallback path.
type writerAdapter struct{ w *bytes.Buffer }

func (a writerAdapter) Write(p []byte) (int, error) { return a.w.Write(p) }

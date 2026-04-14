package proto

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// The Write* helpers below expose an io.Writer-accepting public API but
// take a zero-alloc fast path when the caller supplies a *bytes.Buffer.
//
// Rationale: a straightforward `var buf [N]byte; w.Write(buf[:])` escapes
// to the heap because `w` is an interface — escape analysis cannot prove
// that w.Write does not retain the slice. On the concrete *bytes.Buffer
// path we instead use buf.AvailableBuffer() + binary.LittleEndian.AppendUint*
// to append directly into the buffer's spare capacity with zero allocations.
//
// Fallback semantics are unchanged: bytes written are bit-identical on
// both paths. This preserves every existing caller (codecs, server
// writeRaw against net.Conn, io.Discard in tests).

// WriteUint8 writes a single byte to w.
func WriteUint8(w io.Writer, v uint8) error {
	if buf, ok := w.(*bytes.Buffer); ok {
		slice := append(buf.AvailableBuffer(), v)
		_, _ = buf.Write(slice)
		return nil
	}
	var tmp [1]byte
	tmp[0] = v
	_, err := w.Write(tmp[:])
	return err
}

// WriteUint16 writes a little-endian uint16 to w.
func WriteUint16(w io.Writer, v uint16) error {
	if buf, ok := w.(*bytes.Buffer); ok {
		slice := binary.LittleEndian.AppendUint16(buf.AvailableBuffer(), v)
		_, _ = buf.Write(slice)
		return nil
	}
	var tmp [2]byte
	binary.LittleEndian.PutUint16(tmp[:], v)
	_, err := w.Write(tmp[:])
	return err
}

// WriteUint32 writes a little-endian uint32 to w.
func WriteUint32(w io.Writer, v uint32) error {
	if buf, ok := w.(*bytes.Buffer); ok {
		slice := binary.LittleEndian.AppendUint32(buf.AvailableBuffer(), v)
		_, _ = buf.Write(slice)
		return nil
	}
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], v)
	_, err := w.Write(tmp[:])
	return err
}

// WriteUint64 writes a little-endian uint64 to w.
func WriteUint64(w io.Writer, v uint64) error {
	if buf, ok := w.(*bytes.Buffer); ok {
		slice := binary.LittleEndian.AppendUint64(buf.AvailableBuffer(), v)
		_, _ = buf.Write(slice)
		return nil
	}
	var tmp [8]byte
	binary.LittleEndian.PutUint64(tmp[:], v)
	_, err := w.Write(tmp[:])
	return err
}

// WriteString writes a 9P length-prefixed string to w. The string is
// encoded as length[2] + data[length]. It returns an error if the string
// exceeds MaxStringLen (65535) bytes.
//
// The MaxStringLen validation runs BEFORE the *bytes.Buffer type
// assertion so both the fast path and the io.Writer fallback enforce the
// same length bound identically.
func WriteString(w io.Writer, s string) error {
	if len(s) > MaxStringLen {
		return fmt.Errorf("string length %d exceeds max %d", len(s), MaxStringLen)
	}
	if buf, ok := w.(*bytes.Buffer); ok {
		slice := binary.LittleEndian.AppendUint16(buf.AvailableBuffer(), uint16(len(s)))
		slice = append(slice, s...)
		_, _ = buf.Write(slice)
		return nil
	}
	if err := WriteUint16(w, uint16(len(s))); err != nil {
		return fmt.Errorf("write string length: %w", err)
	}
	if len(s) > 0 {
		if _, err := io.WriteString(w, s); err != nil {
			return fmt.Errorf("write string data: %w", err)
		}
	}
	return nil
}

// WriteQID writes a QID to w in wire format: type[1] + version[4] + path[8].
func WriteQID(w io.Writer, q QID) error {
	if buf, ok := w.(*bytes.Buffer); ok {
		slice := append(buf.AvailableBuffer(), byte(q.Type))
		slice = binary.LittleEndian.AppendUint32(slice, q.Version)
		slice = binary.LittleEndian.AppendUint64(slice, q.Path)
		_, _ = buf.Write(slice)
		return nil
	}
	if err := WriteUint8(w, uint8(q.Type)); err != nil {
		return fmt.Errorf("write qid type: %w", err)
	}
	if err := WriteUint32(w, q.Version); err != nil {
		return fmt.Errorf("write qid version: %w", err)
	}
	if err := WriteUint64(w, q.Path); err != nil {
		return fmt.Errorf("write qid path: %w", err)
	}
	return nil
}

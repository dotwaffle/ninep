package proto

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/dotwaffle/ninep/internal/bufpool"
)

// ReadUint8 reads a single byte from r.
// When r is a *bytes.Reader, uses ReadByte to avoid the temp-buffer heap
// escape that io.ReadFull causes through the io.Reader interface.
func ReadUint8(r io.Reader) (uint8, error) {
	if br, ok := r.(*bytes.Reader); ok {
		b, err := br.ReadByte()
		return b, err
	}
	var buf [1]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return buf[0], nil
}

// ReadUint16 reads a little-endian uint16 from r.
// The *bytes.Reader fast path calls Read on the concrete type: the buffer
// does not escape through an interface, so it stays on the stack.
func ReadUint16(r io.Reader) (uint16, error) {
	if br, ok := r.(*bytes.Reader); ok {
		var buf [2]byte
		if br.Len() == 0 {
			return 0, io.EOF
		}
		if n, _ := br.Read(buf[:]); n < 2 {
			return 0, io.ErrUnexpectedEOF
		}
		return binary.LittleEndian.Uint16(buf[:]), nil
	}
	var buf [2]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(buf[:]), nil
}

// ReadUint32 reads a little-endian uint32 from r.
// See ReadUint16 for the fast-path rationale.
func ReadUint32(r io.Reader) (uint32, error) {
	if br, ok := r.(*bytes.Reader); ok {
		var buf [4]byte
		if br.Len() == 0 {
			return 0, io.EOF
		}
		if n, _ := br.Read(buf[:]); n < 4 {
			return 0, io.ErrUnexpectedEOF
		}
		return binary.LittleEndian.Uint32(buf[:]), nil
	}
	var buf [4]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(buf[:]), nil
}

// ReadUint64 reads a little-endian uint64 from r.
// See ReadUint16 for the fast-path rationale.
func ReadUint64(r io.Reader) (uint64, error) {
	if br, ok := r.(*bytes.Reader); ok {
		var buf [8]byte
		if br.Len() == 0 {
			return 0, io.EOF
		}
		if n, _ := br.Read(buf[:]); n < 8 {
			return 0, io.ErrUnexpectedEOF
		}
		return binary.LittleEndian.Uint64(buf[:]), nil
	}
	var buf [8]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(buf[:]), nil
}

// ReadString reads a 9P length-prefixed string from r. The string is encoded
// as length[2] + data[length].
//
// Uses a pooled scratch buffer (bufpool.stringBufPool) to avoid the per-call
// make([]byte, length) allocation. The final string(*scratch) conversion
// allocates new memory (strings are immutable in Go), so scratch is safe to
// return to the pool on defer -- the returned string does not alias scratch.
func ReadString(r io.Reader) (string, error) {
	length, err := ReadUint16(r)
	if err != nil {
		return "", fmt.Errorf("read string length: %w", err)
	}
	if length == 0 {
		return "", nil
	}
	scratch := bufpool.GetStringBuf(int(length))
	defer bufpool.PutStringBuf(scratch)
	*scratch = (*scratch)[:length]
	if _, err := io.ReadFull(r, *scratch); err != nil {
		return "", fmt.Errorf("read string data: %w", err)
	}
	return string(*scratch), nil
}

// ReadFid reads a fid[4] field from r. It exists so message decoders
// assign fid-typed fields directly instead of repeating the
// uint32-then-convert dance, which the type system cannot check.
func ReadFid(r io.Reader) (Fid, error) {
	v, err := ReadUint32(r)
	return Fid(v), err
}

// ReadData reads exactly count bytes from r into a freshly allocated slice.
// Callers must bound count (e.g. against MaxDataSize) before calling.
//
// When r is a *bytes.Reader -- the path DecodeFrame always uses -- a count
// exceeding the remaining bytes is rejected before the allocation, so a
// crafted frame with an inflated count cannot force a large transient
// allocation from a few bytes of input.
func ReadData(r io.Reader, count uint32) ([]byte, error) {
	if count == 0 {
		return []byte{}, nil
	}
	if br, ok := r.(*bytes.Reader); ok && uint64(count) > uint64(br.Len()) {
		return nil, fmt.Errorf("read data: count %d exceeds remaining %d bytes: %w",
			count, br.Len(), io.ErrUnexpectedEOF)
	}
	data := make([]byte, count)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("read data: %w", err)
	}
	return data, nil
}

// ReadQID reads a QID from r in wire format: type[1] + version[4] + path[8].
func ReadQID(r io.Reader) (QID, error) {
	t, err := ReadUint8(r)
	if err != nil {
		return QID{}, fmt.Errorf("read qid type: %w", err)
	}
	version, err := ReadUint32(r)
	if err != nil {
		return QID{}, fmt.Errorf("read qid version: %w", err)
	}
	path, err := ReadUint64(r)
	if err != nil {
		return QID{}, fmt.Errorf("read qid path: %w", err)
	}
	return QID{Type: QIDType(t), Version: version, Path: path}, nil
}

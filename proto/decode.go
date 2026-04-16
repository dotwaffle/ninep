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
func ReadUint16(r io.Reader) (uint16, error) {
	if br, ok := r.(*bytes.Reader); ok {
		if br.Len() < 2 {
			return 0, io.ErrUnexpectedEOF
		}
		b0, _ := br.ReadByte()
		b1, _ := br.ReadByte()
		return uint16(b0) | uint16(b1)<<8, nil
	}
	var buf [2]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(buf[:]), nil
}

// ReadUint32 reads a little-endian uint32 from r.
func ReadUint32(r io.Reader) (uint32, error) {
	if br, ok := r.(*bytes.Reader); ok {
		if br.Len() < 4 {
			return 0, io.ErrUnexpectedEOF
		}
		b0, _ := br.ReadByte()
		b1, _ := br.ReadByte()
		b2, _ := br.ReadByte()
		b3, _ := br.ReadByte()
		return uint32(b0) | uint32(b1)<<8 | uint32(b2)<<16 | uint32(b3)<<24, nil
	}
	var buf [4]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(buf[:]), nil
}

// ReadUint64 reads a little-endian uint64 from r.
func ReadUint64(r io.Reader) (uint64, error) {
	if br, ok := r.(*bytes.Reader); ok {
		if br.Len() < 8 {
			return 0, io.ErrUnexpectedEOF
		}
		b0, _ := br.ReadByte()
		b1, _ := br.ReadByte()
		b2, _ := br.ReadByte()
		b3, _ := br.ReadByte()
		b4, _ := br.ReadByte()
		b5, _ := br.ReadByte()
		b6, _ := br.ReadByte()
		b7, _ := br.ReadByte()
		return uint64(b0) | uint64(b1)<<8 | uint64(b2)<<16 | uint64(b3)<<24 |
			uint64(b4)<<32 | uint64(b5)<<40 | uint64(b6)<<48 | uint64(b7)<<56, nil
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

package proto

import (
	"encoding/binary"
	"fmt"
	"io"
)

// WriteUint8 writes a single byte to w.
func WriteUint8(w io.Writer, v uint8) error {
	var buf [1]byte
	buf[0] = v
	_, err := w.Write(buf[:])
	return err
}

// WriteUint16 writes a little-endian uint16 to w.
func WriteUint16(w io.Writer, v uint16) error {
	var buf [2]byte
	binary.LittleEndian.PutUint16(buf[:], v)
	_, err := w.Write(buf[:])
	return err
}

// WriteUint32 writes a little-endian uint32 to w.
func WriteUint32(w io.Writer, v uint32) error {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], v)
	_, err := w.Write(buf[:])
	return err
}

// WriteUint64 writes a little-endian uint64 to w.
func WriteUint64(w io.Writer, v uint64) error {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	_, err := w.Write(buf[:])
	return err
}

// WriteString writes a 9P length-prefixed string to w. The string is encoded
// as length[2] + data[length]. It returns an error if the string exceeds
// MaxStringLen (65535) bytes.
func WriteString(w io.Writer, s string) error {
	if len(s) > MaxStringLen {
		return fmt.Errorf("string length %d exceeds max %d", len(s), MaxStringLen)
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

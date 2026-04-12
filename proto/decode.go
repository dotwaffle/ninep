package proto

import (
	"encoding/binary"
	"fmt"
	"io"
)

// ReadUint8 reads a single byte from r.
func ReadUint8(r io.Reader) (uint8, error) {
	var buf [1]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return buf[0], nil
}

// ReadUint16 reads a little-endian uint16 from r.
func ReadUint16(r io.Reader) (uint16, error) {
	var buf [2]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(buf[:]), nil
}

// ReadUint32 reads a little-endian uint32 from r.
func ReadUint32(r io.Reader) (uint32, error) {
	var buf [4]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(buf[:]), nil
}

// ReadUint64 reads a little-endian uint64 from r.
func ReadUint64(r io.Reader) (uint64, error) {
	var buf [8]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(buf[:]), nil
}

// ReadString reads a 9P length-prefixed string from r. The string is encoded
// as length[2] + data[length].
func ReadString(r io.Reader) (string, error) {
	length, err := ReadUint16(r)
	if err != nil {
		return "", fmt.Errorf("read string length: %w", err)
	}
	if length == 0 {
		return "", nil
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return "", fmt.Errorf("read string data: %w", err)
	}
	return string(data), nil
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

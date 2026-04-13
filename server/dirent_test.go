package server

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/dotwaffle/ninep/proto"
)

func TestEncodeDirentsSingle(t *testing.T) {
	t.Parallel()

	dirents := []proto.Dirent{
		{
			QID:    proto.QID{Type: proto.QTFILE, Path: 42, Version: 1},
			Offset: 1,
			Type:   0,
			Name:   "hello",
		},
	}

	// Expected size: QIDSize(13) + offset(8) + type(1) + namelen(2) + name(5) = 29
	data, count := EncodeDirents(dirents, 1024)
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if len(data) != 29 {
		t.Fatalf("len(data) = %d, want 29", len(data))
	}

	// Manually verify wire bytes.
	r := bytes.NewReader(data)

	// QID: type[1] + version[4] + path[8]
	var qidType uint8
	_ = binary.Read(r, binary.LittleEndian, &qidType)
	if qidType != uint8(proto.QTFILE) {
		t.Errorf("qid type = %d, want %d", qidType, proto.QTFILE)
	}
	var qidVersion uint32
	_ = binary.Read(r, binary.LittleEndian, &qidVersion)
	if qidVersion != 1 {
		t.Errorf("qid version = %d, want 1", qidVersion)
	}
	var qidPath uint64
	_ = binary.Read(r, binary.LittleEndian, &qidPath)
	if qidPath != 42 {
		t.Errorf("qid path = %d, want 42", qidPath)
	}

	// Offset.
	var offset uint64
	_ = binary.Read(r, binary.LittleEndian, &offset)
	if offset != 1 {
		t.Errorf("offset = %d, want 1", offset)
	}

	// Type.
	var dtype uint8
	_ = binary.Read(r, binary.LittleEndian, &dtype)
	if dtype != 0 {
		t.Errorf("type = %d, want 0", dtype)
	}

	// Name: len[2] + bytes.
	var nameLen uint16
	_ = binary.Read(r, binary.LittleEndian, &nameLen)
	if nameLen != 5 {
		t.Errorf("name len = %d, want 5", nameLen)
	}
	nameBytes := make([]byte, nameLen)
	_, _ = r.Read(nameBytes)
	if string(nameBytes) != "hello" {
		t.Errorf("name = %q, want %q", string(nameBytes), "hello")
	}

	if r.Len() != 0 {
		t.Errorf("remaining bytes = %d, want 0", r.Len())
	}
}

func TestEncodeDirentsMultiple(t *testing.T) {
	t.Parallel()

	dirents := []proto.Dirent{
		{QID: proto.QID{Type: proto.QTFILE, Path: 1}, Offset: 1, Type: 0, Name: "a"},
		{QID: proto.QID{Type: proto.QTDIR, Path: 2}, Offset: 2, Type: 4, Name: "bb"},
		{QID: proto.QID{Type: proto.QTFILE, Path: 3}, Offset: 3, Type: 0, Name: "ccc"},
	}

	data, count := EncodeDirents(dirents, 4096)
	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}

	// Entry sizes: 13+8+1+2+1=25, 13+8+1+2+2=26, 13+8+1+2+3=27  total=78
	if len(data) != 78 {
		t.Errorf("len(data) = %d, want 78", len(data))
	}
}

func TestEncodeDirentsMaxBytesPartial(t *testing.T) {
	t.Parallel()

	dirents := []proto.Dirent{
		{QID: proto.QID{Path: 1}, Offset: 1, Type: 0, Name: "a"},    // 25 bytes
		{QID: proto.QID{Path: 2}, Offset: 2, Type: 0, Name: "bb"},   // 26 bytes
		{QID: proto.QID{Path: 3}, Offset: 3, Type: 0, Name: "ccc"},  // 27 bytes
	}

	// Only enough room for first two entries (25+26=51).
	data, count := EncodeDirents(dirents, 51)
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
	if len(data) != 51 {
		t.Errorf("len(data) = %d, want 51", len(data))
	}
}

func TestEncodeDirentsEmpty(t *testing.T) {
	t.Parallel()

	data, count := EncodeDirents(nil, 1024)
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
	if len(data) != 0 {
		t.Errorf("len(data) = %d, want 0", len(data))
	}
}

func TestEncodeDirentsRoundTrip(t *testing.T) {
	t.Parallel()

	dirents := []proto.Dirent{
		{QID: proto.QID{Type: proto.QTDIR, Path: 100, Version: 5}, Offset: 1, Type: 4, Name: "docs"},
		{QID: proto.QID{Type: proto.QTFILE, Path: 200, Version: 0}, Offset: 2, Type: 0, Name: "README.md"},
	}

	data, count := EncodeDirents(dirents, 4096)
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}

	// Manually decode and verify round-trip.
	r := bytes.NewReader(data)
	for i := range count {
		var qidType uint8
		var qidVersion uint32
		var qidPath uint64
		var offset uint64
		var dtype uint8
		var nameLen uint16

		_ = binary.Read(r, binary.LittleEndian, &qidType)
		_ = binary.Read(r, binary.LittleEndian, &qidVersion)
		_ = binary.Read(r, binary.LittleEndian, &qidPath)
		_ = binary.Read(r, binary.LittleEndian, &offset)
		_ = binary.Read(r, binary.LittleEndian, &dtype)
		_ = binary.Read(r, binary.LittleEndian, &nameLen)

		nameBytes := make([]byte, nameLen)
		_, _ = r.Read(nameBytes)

		d := dirents[i]
		if qidType != uint8(d.QID.Type) {
			t.Errorf("entry %d: qid type = %d, want %d", i, qidType, d.QID.Type)
		}
		if qidVersion != d.QID.Version {
			t.Errorf("entry %d: qid version = %d, want %d", i, qidVersion, d.QID.Version)
		}
		if qidPath != d.QID.Path {
			t.Errorf("entry %d: qid path = %d, want %d", i, qidPath, d.QID.Path)
		}
		if offset != d.Offset {
			t.Errorf("entry %d: offset = %d, want %d", i, offset, d.Offset)
		}
		if dtype != d.Type {
			t.Errorf("entry %d: type = %d, want %d", i, dtype, d.Type)
		}
		if string(nameBytes) != d.Name {
			t.Errorf("entry %d: name = %q, want %q", i, string(nameBytes), d.Name)
		}
	}
}

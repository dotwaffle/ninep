package p9u_test

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9u"
)

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tag  proto.Tag
		msg  proto.Message
	}{
		// 9P2000.u-specific messages.
		{"Rerror", 1, &p9u.Rerror{Ename: "file not found", Errno: proto.ENOENT}},
		{"Rerror_eperm", 2, &p9u.Rerror{Ename: "permission denied", Errno: proto.EPERM}},
		{"Topen", 3, &p9u.Topen{Fid: 10, Mode: 0}},
		{"Ropen", 4, &p9u.Ropen{
			QID:    proto.QID{Type: proto.QTFILE, Version: 1, Path: 42},
			IOUnit: 8192,
		}},
		{"Tcreate_plain", 5, &p9u.Tcreate{
			Fid: 11, Name: "newfile", Perm: 0644, Mode: 0, Extension: "",
		}},
		{"Tcreate_symlink", 6, &p9u.Tcreate{
			Fid: 12, Name: "mylink", Perm: p9u.DMSYMLINK | 0777, Mode: 0, Extension: "/usr/bin/target",
		}},
		{"Tcreate_device", 7, &p9u.Tcreate{
			Fid: 13, Name: "null", Perm: p9u.DMDEVICE | 0666, Mode: 0, Extension: "c 1 3",
		}},
		{"Rcreate", 8, &p9u.Rcreate{
			QID:    proto.QID{Type: proto.QTFILE, Version: 1, Path: 99},
			IOUnit: 4096,
		}},
		{"Tstat", 9, &p9u.Tstat{Fid: 14}},
		{"Rstat", 10, &p9u.Rstat{Stat: p9u.Stat{
			Type:      0,
			Dev:       0,
			QID:       proto.QID{Type: proto.QTFILE, Version: 3, Path: 50},
			Mode:      0100644,
			Atime:     1700000000,
			Mtime:     1700000001,
			Length:    4096,
			Name:      "test.txt",
			UID:       "root",
			GID:       "wheel",
			MUID:      "root",
			Extension: "",
			NUid:      0,
			NGid:      0,
			NMuid:     0,
		}}},
		{"Twstat", 11, &p9u.Twstat{Fid: 15, Stat: p9u.Stat{
			Name:      "renamed.txt",
			UID:       "nobody",
			GID:       "nogroup",
			MUID:      "",
			Extension: "",
			Mode:      0100755,
		}}},
		{"Rwstat", 12, &p9u.Rwstat{}},

		// Shared base messages via p9u.Encode/Decode.
		{"Tversion", 100, &proto.Tversion{Msize: 8192, Version: "9P2000.u"}},
		{"Rversion", 101, &proto.Rversion{Msize: 8192, Version: "9P2000.u"}},
		{"Tauth", 102, &proto.Tauth{Afid: 100, Uname: "root", Aname: "/", NUname: 0}},
		{"Rauth", 103, &proto.Rauth{
			AQid: proto.QID{Type: proto.QTAUTH, Version: 1, Path: 42},
		}},
		{"Tattach", 104, &proto.Tattach{
			Fid: 0, Afid: proto.NoFid, Uname: "root", Aname: "/mnt", NUname: 1000,
		}},
		{"Rattach", 105, &proto.Rattach{
			QID: proto.QID{Type: proto.QTDIR, Version: 1, Path: 1},
		}},
		{"Tflush", 106, &proto.Tflush{OldTag: proto.Tag(5)}},
		{"Rflush", 107, &proto.Rflush{}},
		{"Twalk", 108, &proto.Twalk{Fid: 1, NewFid: 2, Names: []string{"usr", "bin"}}},
		{"Rwalk", 109, &proto.Rwalk{QIDs: []proto.QID{
			{Type: proto.QTDIR, Version: 1, Path: 10},
			{Type: proto.QTDIR, Version: 1, Path: 20},
		}}},
		{"Tread", 110, &proto.Tread{Fid: 3, Offset: 1024, Count: 4096}},
		{"Rread", 111, &proto.Rread{Data: []byte("hello world")}},
		{"Twrite", 112, &proto.Twrite{Fid: 4, Offset: 0, Data: []byte("test data")}},
		{"Rwrite", 113, &proto.Rwrite{Count: 9}},
		{"Tclunk", 114, &proto.Tclunk{Fid: 5}},
		{"Rclunk", 115, &proto.Rclunk{}},
		{"Tremove", 116, &proto.Tremove{Fid: 6}},
		{"Rremove", 117, &proto.Rremove{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Encode.
			var buf bytes.Buffer
			if err := p9u.Encode(&buf, tt.tag, tt.msg); err != nil {
				t.Fatalf("Encode(%s): %v", tt.name, err)
			}

			// Decode.
			gotTag, gotMsg, err := p9u.Decode(&buf)
			if err != nil {
				t.Fatalf("Decode(%s): %v", tt.name, err)
			}

			// Verify tag.
			if gotTag != tt.tag {
				t.Errorf("tag = %d, want %d", gotTag, tt.tag)
			}

			// Verify message type.
			if gotMsg.Type() != tt.msg.Type() {
				t.Errorf("Type() = %v, want %v", gotMsg.Type(), tt.msg.Type())
			}

			// Verify field values.
			if !reflect.DeepEqual(gotMsg, tt.msg) {
				t.Errorf("message mismatch:\ngot  %+v\nwant %+v", gotMsg, tt.msg)
			}
		})
	}
}

func TestStatEncodedSize(t *testing.T) {
	t.Parallel()

	stat := p9u.Stat{
		Type:      0,
		Dev:       0,
		QID:       proto.QID{Type: proto.QTFILE, Version: 3, Path: 50},
		Mode:      0100644,
		Atime:     1700000000,
		Mtime:     1700000001,
		Length:    4096,
		Name:      "test.txt",
		UID:       "root",
		GID:       "wheel",
		MUID:      "root",
		Extension: "",
		NUid:      0,
		NGid:      0,
		NMuid:     0,
	}

	// Encode the stat (EncodeTo writes size[2] + body).
	var buf bytes.Buffer
	if err := stat.EncodeTo(&buf); err != nil {
		t.Fatalf("EncodeTo: %v", err)
	}

	// EncodedSize returns the body length (excluding the 2-byte size prefix).
	encodedSize, err := stat.EncodedSize()
	if err != nil {
		t.Fatalf("EncodedSize: %v", err)
	}

	// The buffer should contain size[2] + body[encodedSize].
	if buf.Len() != int(encodedSize)+2 {
		t.Errorf("buffer len = %d, want %d (encodedSize=%d + 2)", buf.Len(), int(encodedSize)+2, encodedSize)
	}
}

func TestDecodeUnknownType(t *testing.T) {
	t.Parallel()

	// Construct a message with unknown type 255: size=8, type=255, tag=0, body=0x00.
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, uint32(8)) // size
	buf.WriteByte(255)                                     // type (unknown)
	_ = binary.Write(&buf, binary.LittleEndian, uint16(0)) // tag
	buf.WriteByte(0x00)                                    // body byte

	_, _, err := p9u.Decode(&buf)
	if err == nil {
		t.Fatal("expected error for unknown message type, got nil")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error should contain 'unknown', got: %v", err)
	}
}

func TestDecodeTruncated(t *testing.T) {
	t.Parallel()

	// Encode a valid Twalk via p9u.
	msg := &proto.Twalk{Fid: 1, NewFid: 2, Names: []string{"usr", "bin", "ls"}}
	var buf bytes.Buffer
	if err := p9u.Encode(&buf, 42, msg); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// Truncate by 5 bytes.
	data := buf.Bytes()
	if len(data) <= 5 {
		t.Fatal("encoded data too short to truncate")
	}
	truncated := bytes.NewReader(data[:len(data)-5])

	_, _, err := p9u.Decode(truncated)
	if err == nil {
		t.Fatal("expected error for truncated message, got nil")
	}
}

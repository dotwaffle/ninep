package p9l_test

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tag  proto.Tag
		msg  proto.Message
	}{
		// 9P2000.L-specific messages.
		{"Rlerror", 1, &p9l.Rlerror{Ecode: proto.ENOENT}},
		{"Rlerror_ENOTSUPP", 2, &p9l.Rlerror{Ecode: proto.ENOTSUPP}},
		{"Tstatfs", 3, &p9l.Tstatfs{Fid: 7}},
		{"Rstatfs", 4, &p9l.Rstatfs{Stat: proto.FSStat{
			Type: 0x6969, BSize: 4096, Blocks: 1000, BFree: 500,
			BAvail: 400, Files: 200, FFree: 100, FSID: 0xDEADBEEF, NameLen: 255,
		}}},
		{"Tlopen", 5, &p9l.Tlopen{Fid: 10, Flags: 0x0002}},
		{"Rlopen", 6, &p9l.Rlopen{
			QID:    proto.QID{Type: proto.QTFILE, Version: 1, Path: 42},
			IOUnit: 8192,
		}},
		{"Tlcreate", 7, &p9l.Tlcreate{
			Fid: 11, Name: "newfile.txt", Flags: 0x0042, Mode: 0644, GID: 1000,
		}},
		{"Rlcreate", 8, &p9l.Rlcreate{
			QID:    proto.QID{Type: proto.QTFILE, Version: 1, Path: 99},
			IOUnit: 4096,
		}},
		{"Tsymlink", 9, &p9l.Tsymlink{
			DirFid: 12, Name: "link", Target: "/usr/bin/foo", GID: 0,
		}},
		{"Rsymlink", 10, &p9l.Rsymlink{
			QID: proto.QID{Type: proto.QTSYMLINK, Version: 1, Path: 50},
		}},
		{"Tmknod", 11, &p9l.Tmknod{
			DirFid: 13, Name: "null", Mode: 0020666, Major: 1, Minor: 3, GID: 0,
		}},
		{"Rmknod", 12, &p9l.Rmknod{
			QID: proto.QID{Type: proto.QTFILE, Version: 1, Path: 60},
		}},
		{"Trename", 13, &p9l.Trename{Fid: 14, DirFid: 15, Name: "renamed"}},
		{"Rrename", 14, &p9l.Rrename{}},
		{"Treadlink", 15, &p9l.Treadlink{Fid: 16}},
		{"Rreadlink", 16, &p9l.Rreadlink{Target: "/usr/lib/libfoo.so"}},
		{"Tgetattr", 17, &p9l.Tgetattr{Fid: 17, RequestMask: 0x17FF}},
		{"Rgetattr", 18, &p9l.Rgetattr{Attr: proto.Attr{
			Valid:       0x17FF,
			QID:         proto.QID{Type: proto.QTFILE, Version: 5, Path: 100},
			Mode:        0100644,
			UID:         1000,
			GID:         1000,
			NLink:       1,
			RDev:        0,
			Size:        4096,
			BlkSize:     512,
			Blocks:      8,
			ATimeSec:    1700000000,
			ATimeNSec:   123456789,
			MTimeSec:    1700000001,
			MTimeNSec:   987654321,
			CTimeSec:    1700000002,
			CTimeNSec:   111111111,
			BTimeSec:    0,
			BTimeNSec:   0,
			Gen:         0,
			DataVersion: 0,
		}}},
		{"Tsetattr", 19, &p9l.Tsetattr{
			Fid: 18,
			Attr: proto.SetAttr{
				Valid: 0x01, Mode: 0755, UID: 0, GID: 0, Size: 0,
				ATimeSec: 0, ATimeNSec: 0, MTimeSec: 0, MTimeNSec: 0,
			},
		}},
		{"Rsetattr", 20, &p9l.Rsetattr{}},
		{"Txattrwalk", 21, &p9l.Txattrwalk{Fid: 19, NewFid: 20, Name: "user.mime_type"}},
		{"Rxattrwalk", 22, &p9l.Rxattrwalk{Size: 1024}},
		{"Txattrcreate", 23, &p9l.Txattrcreate{
			Fid: 21, Name: "user.test", AttrSize: 256, Flags: 0x01,
		}},
		{"Rxattrcreate", 24, &p9l.Rxattrcreate{}},
		{"Treaddir", 25, &p9l.Treaddir{Fid: 22, Offset: 0, Count: 8192}},
		{"Rreaddir", 26, &p9l.Rreaddir{Data: []byte{1, 2, 3, 4, 5}}},
		{"Tfsync", 27, &p9l.Tfsync{Fid: 23, DataSync: 1}},
		{"Rfsync", 28, &p9l.Rfsync{}},
		{"Tlock", 29, &p9l.Tlock{
			Fid: 24, LockType: 0, Flags: 0x01, Start: 0, Length: 100,
			ProcID: 1234, ClientID: "host1",
		}},
		{"Rlock", 30, &p9l.Rlock{Status: 0}},
		{"Tgetlock", 31, &p9l.Tgetlock{
			Fid: 25, LockType: 1, Start: 0, Length: 0, ProcID: 5678, ClientID: "host2",
		}},
		{"Rgetlock", 32, &p9l.Rgetlock{
			LockType: 2, Start: 50, Length: 200, ProcID: 9999, ClientID: "host3",
		}},
		{"Tlink", 33, &p9l.Tlink{DirFid: 26, Fid: 27, Name: "hardlink"}},
		{"Rlink", 34, &p9l.Rlink{}},
		{"Tmkdir", 35, &p9l.Tmkdir{DirFid: 28, Name: "newdir", Mode: 0755, GID: 1000}},
		{"Rmkdir", 36, &p9l.Rmkdir{
			QID: proto.QID{Type: proto.QTDIR, Version: 1, Path: 200},
		}},
		{"Trenameat", 37, &p9l.Trenameat{
			OldDirFid: 29, OldName: "old.txt", NewDirFid: 30, NewName: "new.txt",
		}},
		{"Rrenameat", 38, &p9l.Rrenameat{}},
		{"Tunlinkat", 39, &p9l.Tunlinkat{DirFid: 31, Name: "delete.txt", Flags: 0x200}},
		{"Runlinkat", 40, &p9l.Runlinkat{}},

		// Shared base messages via p9l.Encode/Decode.
		{"Tversion", 100, &proto.Tversion{Msize: 8192, Version: "9P2000.L"}},
		{"Rversion", 101, &proto.Rversion{Msize: 8192, Version: "9P2000.L"}},
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
			if err := p9l.Encode(&buf, tt.tag, tt.msg); err != nil {
				t.Fatalf("Encode(%s): %v", tt.name, err)
			}

			// Decode.
			gotTag, gotMsg, err := p9l.Decode(&buf)
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

func TestDecodeUnknownType(t *testing.T) {
	t.Parallel()

	// Construct a message with unknown type 255: size=8, type=255, tag=0, body=0x00.
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint32(8))  // size
	buf.WriteByte(255)                                    // type (unknown)
	binary.Write(&buf, binary.LittleEndian, uint16(0))   // tag
	buf.WriteByte(0x00)                                   // body byte

	_, _, err := p9l.Decode(&buf)
	if err == nil {
		t.Fatal("expected error for unknown message type, got nil")
	}
	if !containsSubstring(err.Error(), "unknown") {
		t.Errorf("error should contain 'unknown', got: %v", err)
	}
}

func TestDecodeTruncated(t *testing.T) {
	t.Parallel()

	// Encode a valid Twalk.
	msg := &proto.Twalk{Fid: 1, NewFid: 2, Names: []string{"usr", "bin", "ls"}}
	var buf bytes.Buffer
	if err := p9l.Encode(&buf, 42, msg); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// Truncate by 5 bytes.
	data := buf.Bytes()
	if len(data) <= 5 {
		t.Fatal("encoded data too short to truncate")
	}
	truncated := bytes.NewReader(data[:len(data)-5])

	_, _, err := p9l.Decode(truncated)
	if err == nil {
		t.Fatal("expected error for truncated message, got nil")
	}
}

func TestDecodeMinSize(t *testing.T) {
	t.Parallel()

	// Construct a message with size < HeaderSize (7): size=6, type=0, tag=0.
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint32(6)) // size (too small)
	buf.WriteByte(0)                                    // type
	binary.Write(&buf, binary.LittleEndian, uint16(0)) // tag

	_, _, err := p9l.Decode(&buf)
	if err == nil {
		t.Fatal("expected error for undersized message, got nil")
	}
	if !containsSubstring(err.Error(), "too small") {
		t.Errorf("error should contain 'too small', got: %v", err)
	}
}

// containsSubstring checks if s contains substr (simple helper to avoid
// importing strings for one use).
func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

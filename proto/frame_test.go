package proto

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

// baseNewMessage adapts NewBaseMessage to DecodeFrame's constructor shape,
// covering the dialect-shared message types used by these tests.
func baseNewMessage(t MessageType) (Message, error) {
	m, ok := NewBaseMessage(t)
	if !ok {
		return nil, fmt.Errorf("unknown message type %d", t)
	}
	return m, nil
}

// encodeRawFrame builds a frame with an arbitrary declared size so tests can
// lie about it independently of the actual body length.
func encodeRawFrame(declaredSize uint32, msgType MessageType, tag Tag, body []byte) []byte {
	frame := make([]byte, 0, int(HeaderSize)+len(body))
	frame = binary.LittleEndian.AppendUint32(frame, declaredSize)
	frame = append(frame, uint8(msgType))
	frame = binary.LittleEndian.AppendUint16(frame, uint16(tag))
	return append(frame, body...)
}

func TestDecodeFrameMalformed(t *testing.T) {
	t.Parallel()

	// A well-formed Tclunk body: fid[4].
	fidBody := []byte{1, 0, 0, 0}

	tests := []struct {
		name    string
		frame   []byte
		wantErr string
	}{
		{
			name:    "size below header minimum",
			frame:   encodeRawFrame(HeaderSize-1, TypeTclunk, 1, nil),
			wantErr: "too small",
		},
		{
			name:    "size above MaxMessageSize",
			frame:   encodeRawFrame(MaxMessageSize+1, TypeTclunk, 1, fidBody),
			wantErr: "exceeds maximum",
		},
		{
			name: "overstated size leaves trailing bytes",
			frame: encodeRawFrame(HeaderSize+4+3, TypeTclunk, 1,
				append(append([]byte{}, fidBody...), 0xde, 0xad, 0xbe)),
			wantErr: "trailing bytes",
		},
		{
			name:    "understated size truncates body",
			frame:   encodeRawFrame(HeaderSize+2, TypeTclunk, 1, fidBody[:2]),
			wantErr: "decode Tclunk body",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := DecodeFrame(bytes.NewReader(tc.frame), baseNewMessage)
			if err == nil {
				t.Fatalf("DecodeFrame accepted malformed frame %x", tc.frame)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}

	t.Run("well-formed frame still decodes", func(t *testing.T) {
		t.Parallel()
		frame := encodeRawFrame(HeaderSize+4, TypeTclunk, 7, fidBody)
		tag, msg, err := DecodeFrame(bytes.NewReader(frame), baseNewMessage)
		if err != nil {
			t.Fatalf("DecodeFrame: %v", err)
		}
		if tag != 7 {
			t.Errorf("tag = %d, want 7", tag)
		}
		clunk, ok := msg.(*Tclunk)
		if !ok || clunk.Fid != 1 {
			t.Errorf("decoded %#v, want Tclunk{Fid: 1}", msg)
		}
	})
}

func TestTwriteDecodeFromBuf(t *testing.T) {
	t.Parallel()

	// A valid body: fid=5, offset=64, count=4, data "abcd".
	valid := func() []byte {
		b := make([]byte, 0, 20)
		b = binary.LittleEndian.AppendUint32(b, 5)
		b = binary.LittleEndian.AppendUint64(b, 64)
		b = binary.LittleEndian.AppendUint32(b, 4)
		return append(b, 'a', 'b', 'c', 'd')
	}

	t.Run("exact fit", func(t *testing.T) {
		t.Parallel()
		var m Twrite
		if err := m.DecodeFromBuf(valid()); err != nil {
			t.Fatalf("DecodeFromBuf: %v", err)
		}
		if m.Fid != 5 || m.Offset != 64 || string(m.Data) != "abcd" {
			t.Errorf("decoded %+v, want fid=5 offset=64 data=abcd", m)
		}
	})

	t.Run("body shorter than fixed fields", func(t *testing.T) {
		t.Parallel()
		var m Twrite
		if err := m.DecodeFromBuf(valid()[:15]); err == nil {
			t.Fatal("expected error for 15-byte body")
		}
	})

	t.Run("count exceeds MaxDataSize", func(t *testing.T) {
		t.Parallel()
		b := valid()
		binary.LittleEndian.PutUint32(b[12:16], MaxDataSize+1)
		var m Twrite
		if err := m.DecodeFromBuf(b); err == nil {
			t.Fatal("expected error for count above MaxDataSize")
		}
	})

	t.Run("count exceeds available data", func(t *testing.T) {
		t.Parallel()
		b := valid()
		binary.LittleEndian.PutUint32(b[12:16], 5)
		var m Twrite
		if err := m.DecodeFromBuf(b); err == nil {
			t.Fatal("expected error for count exceeding trailing bytes")
		}
	})

	t.Run("data aliases the input buffer", func(t *testing.T) {
		t.Parallel()
		b := valid()
		var m Twrite
		if err := m.DecodeFromBuf(b); err != nil {
			t.Fatalf("DecodeFromBuf: %v", err)
		}
		b[16] = 'z'
		if string(m.Data) != "zbcd" {
			t.Errorf("Data = %q; the zero-copy aliasing contract is broken", m.Data)
		}
	})
}

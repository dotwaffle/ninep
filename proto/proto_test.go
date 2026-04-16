package proto

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestErrno(t *testing.T) {
	t.Parallel()
	tests := []struct {
		errno Errno
		want  string
	}{
		{EPERM, "operation not permitted"},
		{ENOENT, "no such file or directory"},
		{EIO, "input/output error"},
		{ENOSYS, "function not implemented"},
		{ENOTSUP, "operation not supported"},
		{ENOTSUPP, "operation not supported (kernel)"},
		{Errno(0), "errno 0"},
		{Errno(9999), "errno 9999"},
	}
	for _, tt := range tests {
		if got := tt.errno.Error(); got != tt.want {
			t.Errorf("Errno(%d).Error() = %q, want %q", uint32(tt.errno), got, tt.want)
		}
	}
}

func TestErrnoIs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		err    error
		target error
		want   bool
	}{
		{"EPERM==EPERM", EPERM, EPERM, true},
		{"ENOTSUP==ENOTSUP", ENOTSUP, ENOTSUP, true},
		{"ENOTSUP!=ENOTSUPP", ENOTSUP, ENOTSUPP, false},
		{"ENOTSUPP!=ENOTSUP", ENOTSUPP, ENOTSUP, false},
		{"ENOTSUP==EOPNOTSUPP", ENOTSUP, EOPNOTSUPP, true}, // Both are 95.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := errors.Is(tt.err, tt.target); got != tt.want {
				t.Errorf("errors.Is(%v, %v) = %v, want %v", tt.err, tt.target, got, tt.want)
			}
		})
	}
}

func TestErrnoSentinels(t *testing.T) {
	t.Parallel()
	// Verify named sentinel vars map to expected errno values.
	if ErrPermission != EPERM {
		t.Errorf("ErrPermission = %d, want %d", ErrPermission, EPERM)
	}
	if ErrNotFound != ENOENT {
		t.Errorf("ErrNotFound = %d, want %d", ErrNotFound, ENOENT)
	}
	if ErrIO != EIO {
		t.Errorf("ErrIO = %d, want %d", ErrIO, EIO)
	}
	if ErrNoSys != ENOSYS {
		t.Errorf("ErrNoSys = %d, want %d", ErrNoSys, ENOSYS)
	}
	if ErrNotSupported != ENOTSUP {
		t.Errorf("ErrNotSupported = %d, want %d", ErrNotSupported, ENOTSUP)
	}
}

func TestErrnoDistinctValues(t *testing.T) {
	t.Parallel()
	// Critical: ENOTSUPP(524) and ENOTSUP(95) must be distinct.
	if ENOTSUPP == ENOTSUP {
		t.Fatal("ENOTSUPP and ENOTSUP must be distinct values")
	}
	if uint32(ENOTSUPP) != 524 {
		t.Errorf("ENOTSUPP = %d, want 524", ENOTSUPP)
	}
	if uint32(ENOTSUP) != 95 {
		t.Errorf("ENOTSUP = %d, want 95", ENOTSUP)
	}
}

func TestStringRoundTrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		s    string
	}{
		{"empty", ""},
		{"hello", "hello"},
		{"protocol", "9P2000.L"},
		{"max_length", strings.Repeat("x", MaxStringLen)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			if err := WriteString(&buf, tt.s); err != nil {
				t.Fatalf("WriteString(%q): %v", tt.s, err)
			}
			got, err := ReadString(&buf)
			if err != nil {
				t.Fatalf("ReadString: %v", err)
			}
			if got != tt.s {
				t.Errorf("ReadString = %q, want %q", got, tt.s)
			}
		})
	}
}

func TestStringTooLong(t *testing.T) {
	t.Parallel()
	s := strings.Repeat("x", MaxStringLen+1)
	var buf bytes.Buffer
	if err := WriteString(&buf, s); err == nil {
		t.Fatal("WriteString with 65536-byte string should return error")
	}
}

func TestQIDRoundTrip(t *testing.T) {
	t.Parallel()
	q := QID{Type: QTDIR, Version: 42, Path: 0xDEADBEEF01234567}
	var buf bytes.Buffer
	if err := WriteQID(&buf, q); err != nil {
		t.Fatalf("WriteQID: %v", err)
	}
	if buf.Len() != QIDSize {
		t.Errorf("QID wire size = %d, want %d", buf.Len(), QIDSize)
	}
	got, err := ReadQID(&buf)
	if err != nil {
		t.Fatalf("ReadQID: %v", err)
	}
	if got != q {
		t.Errorf("ReadQID = %+v, want %+v", got, q)
	}
}

func TestUintHelpers(t *testing.T) {
	t.Parallel()

	t.Run("uint8", func(t *testing.T) {
		t.Parallel()
		for _, v := range []uint8{0, 1, 42, math.MaxUint8} {
			var buf bytes.Buffer
			if err := WriteUint8(&buf, v); err != nil {
				t.Fatalf("WriteUint8(%d): %v", v, err)
			}
			got, err := ReadUint8(&buf)
			if err != nil {
				t.Fatalf("ReadUint8: %v", err)
			}
			if got != v {
				t.Errorf("ReadUint8 = %d, want %d", got, v)
			}
		}
	})

	t.Run("uint16", func(t *testing.T) {
		t.Parallel()
		for _, v := range []uint16{0, 1, 12345, math.MaxUint16} {
			var buf bytes.Buffer
			if err := WriteUint16(&buf, v); err != nil {
				t.Fatalf("WriteUint16(%d): %v", v, err)
			}
			got, err := ReadUint16(&buf)
			if err != nil {
				t.Fatalf("ReadUint16: %v", err)
			}
			if got != v {
				t.Errorf("ReadUint16 = %d, want %d", got, v)
			}
		}
	})

	t.Run("uint32", func(t *testing.T) {
		t.Parallel()
		for _, v := range []uint32{0, 1, 0xDEADBEEF, math.MaxUint32} {
			var buf bytes.Buffer
			if err := WriteUint32(&buf, v); err != nil {
				t.Fatalf("WriteUint32(%d): %v", v, err)
			}
			got, err := ReadUint32(&buf)
			if err != nil {
				t.Fatalf("ReadUint32: %v", err)
			}
			if got != v {
				t.Errorf("ReadUint32 = %d, want %d", got, v)
			}
		}
	})

	t.Run("uint64", func(t *testing.T) {
		t.Parallel()
		for _, v := range []uint64{0, 1, 0xDEADBEEF01234567, math.MaxUint64} {
			var buf bytes.Buffer
			if err := WriteUint64(&buf, v); err != nil {
				t.Fatalf("WriteUint64(%d): %v", v, err)
			}
			got, err := ReadUint64(&buf)
			if err != nil {
				t.Fatalf("ReadUint64: %v", err)
			}
			if got != v {
				t.Errorf("ReadUint64 = %d, want %d", got, v)
			}
		}
	})
}

func TestSharedMessageRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		msg     Message
		newMsg  func() Message
		msgType MessageType
	}{
		{
			"Tversion",
			&Tversion{Msize: 8192, Version: "9P2000.L"},
			func() Message { return &Tversion{} },
			TypeTversion,
		},
		{
			"Rversion",
			&Rversion{Msize: 8192, Version: "9P2000.L"},
			func() Message { return &Rversion{} },
			TypeRversion,
		},
		{
			"Tauth",
			&Tauth{Afid: 100, Uname: "root", Aname: "/", NUname: 0},
			func() Message { return &Tauth{} },
			TypeTauth,
		},
		{
			"Rauth",
			&Rauth{AQid: QID{Type: QTAUTH, Version: 1, Path: 42}},
			func() Message { return &Rauth{} },
			TypeRauth,
		},
		{
			"Tattach",
			&Tattach{Fid: 0, Afid: NoFid, Uname: "root", Aname: "/mnt", NUname: 1000},
			func() Message { return &Tattach{} },
			TypeTattach,
		},
		{
			"Rattach",
			&Rattach{QID: QID{Type: QTDIR, Version: 1, Path: 1}},
			func() Message { return &Rattach{} },
			TypeRattach,
		},
		{
			"Tflush",
			&Tflush{OldTag: Tag(5)},
			func() Message { return &Tflush{} },
			TypeTflush,
		},
		{
			"Rflush",
			&Rflush{},
			func() Message { return &Rflush{} },
			TypeRflush,
		},
		{
			"Twalk",
			&Twalk{Fid: 1, NewFid: 2, Names: []string{"usr", "bin"}},
			func() Message { return &Twalk{} },
			TypeTwalk,
		},
		{
			"Rwalk",
			&Rwalk{QIDs: []QID{
				{Type: QTDIR, Version: 1, Path: 10},
				{Type: QTDIR, Version: 1, Path: 20},
			}},
			func() Message { return &Rwalk{} },
			TypeRwalk,
		},
		{
			"Tread",
			&Tread{Fid: 3, Offset: 1024, Count: 4096},
			func() Message { return &Tread{} },
			TypeTread,
		},
		{
			"Rread",
			&Rread{Data: []byte("hello world")},
			func() Message { return &Rread{} },
			TypeRread,
		},
		{
			"Twrite",
			&Twrite{Fid: 4, Offset: 0, Data: []byte("test data")},
			func() Message { return &Twrite{} },
			TypeTwrite,
		},
		{
			"Rwrite",
			&Rwrite{Count: 9},
			func() Message { return &Rwrite{} },
			TypeRwrite,
		},
		{
			"Tclunk",
			&Tclunk{Fid: 5},
			func() Message { return &Tclunk{} },
			TypeTclunk,
		},
		{
			"Rclunk",
			&Rclunk{},
			func() Message { return &Rclunk{} },
			TypeRclunk,
		},
		{
			"Tremove",
			&Tremove{Fid: 6},
			func() Message { return &Tremove{} },
			TypeTremove,
		},
		{
			"Rremove",
			&Rremove{},
			func() Message { return &Rremove{} },
			TypeRremove,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Verify Type().
			if got := tt.msg.Type(); got != tt.msgType {
				t.Errorf("Type() = %v, want %v", got, tt.msgType)
			}

			// Encode.
			var buf bytes.Buffer
			if err := tt.msg.EncodeTo(&buf); err != nil {
				t.Fatalf("EncodeTo: %v", err)
			}

			// Decode into a fresh struct.
			decoded := tt.newMsg()
			if err := decoded.DecodeFrom(&buf); err != nil {
				t.Fatalf("DecodeFrom: %v", err)
			}

			// Compare.
			if !reflect.DeepEqual(tt.msg, decoded) {
				t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", decoded, tt.msg)
			}
		})
	}
}

func TestTwalkMaxNames(t *testing.T) {
	t.Parallel()
	names := make([]string, MaxWalkElements+1)
	for i := range names {
		names[i] = "x"
	}
	msg := &Twalk{Fid: 1, NewFid: 2, Names: names}
	var buf bytes.Buffer
	err := msg.EncodeTo(&buf)
	if err == nil {
		t.Fatal("EncodeTo with 17 names should return error")
	}
	if !strings.Contains(err.Error(), "exceeds max") {
		t.Errorf("error = %q, want it to contain 'exceeds max'", err.Error())
	}
}

func TestRwalkMaxQIDs(t *testing.T) {
	t.Parallel()
	qids := make([]QID, MaxWalkElements+1)
	msg := &Rwalk{QIDs: qids}
	var buf bytes.Buffer
	err := msg.EncodeTo(&buf)
	if err == nil {
		t.Fatal("EncodeTo with 17 QIDs should return error")
	}
	if !strings.Contains(err.Error(), "exceeds max") {
		t.Errorf("error = %q, want it to contain 'exceeds max'", err.Error())
	}
}

func TestEmptyBodyMessages(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		msg  Message
	}{
		{"Rflush", &Rflush{}},
		{"Rclunk", &Rclunk{}},
		{"Rremove", &Rremove{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			if err := tt.msg.EncodeTo(&buf); err != nil {
				t.Fatalf("EncodeTo: %v", err)
			}
			if buf.Len() != 0 {
				t.Errorf("body size = %d, want 0", buf.Len())
			}
		})
	}
}

func TestTwalkZeroNames(t *testing.T) {
	t.Parallel()
	msg := &Twalk{Fid: 1, NewFid: 1, Names: nil}
	var buf bytes.Buffer
	if err := msg.EncodeTo(&buf); err != nil {
		t.Fatalf("EncodeTo: %v", err)
	}
	decoded := &Twalk{}
	if err := decoded.DecodeFrom(&buf); err != nil {
		t.Fatalf("DecodeFrom: %v", err)
	}
	if decoded.Fid != 1 || decoded.NewFid != 1 || len(decoded.Names) != 0 {
		t.Errorf("round-trip mismatch: got %+v", decoded)
	}
}

func TestMessageTypeString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mt   MessageType
		want string
	}{
		{TypeTversion, "Tversion"},
		{TypeRversion, "Rversion"},
		{TypeTwalk, "Twalk"},
		{TypeRwalk, "Rwalk"},
		{TypeTlerror, "Tlerror"},
		{MessageType(255), "MessageType(255)"},
	}
	for _, tt := range tests {
		if got := tt.mt.String(); got != tt.want {
			t.Errorf("MessageType(%d).String() = %q, want %q", tt.mt, got, tt.want)
		}
	}
}

func TestConstants(t *testing.T) {
	t.Parallel()
	if HeaderSize != 7 {
		t.Errorf("HeaderSize = %d, want 7", HeaderSize)
	}
	if MaxWalkElements != 16 {
		t.Errorf("MaxWalkElements = %d, want 16", MaxWalkElements)
	}
	if NoTag != Tag(math.MaxUint16) {
		t.Errorf("NoTag = %d, want %d", NoTag, math.MaxUint16)
	}
	if NoFid != Fid(math.MaxUint32) {
		t.Errorf("NoFid = %d, want %d", NoFid, uint32(math.MaxUint32))
	}
	if QIDSize != 13 {
		t.Errorf("QIDSize = %d, want 13", QIDSize)
	}
}

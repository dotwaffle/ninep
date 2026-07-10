package proto

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/dotwaffle/ninep/internal/bufpool"
)

// MaxMessageSize bounds the declared size field of a frame accepted by
// DecodeFrame. It is MaxDataSize (the largest legal Rread/Twrite/Rreaddir
// payload) plus headroom for the small fixed fields that surround the data
// in those messages, so no valid message is ever rejected. DecodeFrame
// checks this bound before reading the body, so a peer cannot hold a
// decode call open reading toward a multi-gigabyte "body": codec users
// have no negotiated msize to fall back on, unlike the framing layer in
// server/conn.go and client/read_loop.go.
const MaxMessageSize = HeaderSize + uint32(MaxDataSize) + 4096

// zeroHeader is copied into the pooled encode buffer to reserve space for
// the size[4]+type[1]+tag[2] header, which is patched in after the body is
// encoded. Read-only; safe to share across concurrent EncodeFrame calls.
var zeroHeader [HeaderSize]byte

// EncodeFrame writes a complete 9P message to w: the size[4]+type[1]+tag[2]
// header followed by msg's encoded body, as a single Write call.
//
// The header and body are assembled in one pooled buffer so the frame goes
// out as one write. Against a raw net.Conn that is one syscall and an
// atomic frame, rather than four separate writes that can interleave with
// another writer's frame on the same connection.
func EncodeFrame(w io.Writer, tag Tag, msg Message) error {
	buf := bufpool.GetBuf()
	defer bufpool.PutBuf(buf)

	buf.Write(zeroHeader[:])
	if err := msg.EncodeTo(buf); err != nil {
		return fmt.Errorf("encode %s body: %w", msg.Type(), err)
	}

	b := buf.Bytes()
	binary.LittleEndian.PutUint32(b[0:4], uint32(len(b)))
	b[4] = uint8(msg.Type())
	binary.LittleEndian.PutUint16(b[5:7], uint16(tag))

	if _, err := w.Write(b); err != nil {
		return fmt.Errorf("encode frame: %w", err)
	}
	return nil
}

// DecodeFrame reads a complete 9P message from r. newMessage constructs the
// correctly-typed, dialect-specific body for the decoded message type.
//
// The body is read fully into a pooled buffer and decoded through a
// *bytes.Reader, which lets ReadUint8/16/32/64 take their zero-alloc
// fast path. DecodeFrame also rejects any bytes left in the body after
// DecodeFrom returns: without this check, a peer that overstates the
// frame size would leave trailing bytes in r that the next DecodeFrame
// call parses as the following frame's header, silently desynchronizing
// the stream instead of returning an error.
func DecodeFrame(r io.Reader, newMessage func(MessageType) (Message, error)) (Tag, Message, error) {
	size, err := ReadUint32(r)
	if err != nil {
		return 0, nil, fmt.Errorf("decode size: %w", err)
	}
	if size < HeaderSize {
		return 0, nil, fmt.Errorf("message size %d too small (minimum %d)", size, HeaderSize)
	}
	if size > MaxMessageSize {
		return 0, nil, fmt.Errorf("message size %d exceeds maximum %d", size, MaxMessageSize)
	}

	msgType, err := ReadUint8(r)
	if err != nil {
		return 0, nil, fmt.Errorf("decode type: %w", err)
	}

	tag, err := ReadUint16(r)
	if err != nil {
		return 0, nil, fmt.Errorf("decode tag: %w", err)
	}

	msg, err := newMessage(MessageType(msgType))
	if err != nil {
		return 0, nil, err
	}

	bodySize := int(size - HeaderSize)
	body := bufpool.GetMsgBuf(bodySize)
	defer bufpool.PutMsgBuf(body)
	buf := (*body)[:bodySize]
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, nil, fmt.Errorf("decode %s body: %w", msg.Type(), err)
	}

	br := bytes.NewReader(buf)
	if err := msg.DecodeFrom(br); err != nil {
		return 0, nil, fmt.Errorf("decode %s body: %w", msg.Type(), err)
	}
	if br.Len() != 0 {
		return 0, nil, fmt.Errorf("decode %s body: %d trailing bytes", msg.Type(), br.Len())
	}

	return Tag(tag), msg, nil
}

// NewBaseMessage returns a zero-value struct for a message type shared by
// both 9P dialects (version negotiation, walk, read/write, clunk, remove,
// flush, attach, auth). It reports false if t is not one of these shared
// types, so p9l.newMessage/p9u.newMessage can fall back to it after their
// own dialect-specific cases.
func NewBaseMessage(t MessageType) (Message, bool) {
	switch t {
	case TypeTversion:
		return &Tversion{}, true
	case TypeRversion:
		return &Rversion{}, true
	case TypeTauth:
		return &Tauth{}, true
	case TypeRauth:
		return &Rauth{}, true
	case TypeTattach:
		return &Tattach{}, true
	case TypeRattach:
		return &Rattach{}, true
	case TypeTflush:
		return &Tflush{}, true
	case TypeRflush:
		return &Rflush{}, true
	case TypeTwalk:
		return &Twalk{}, true
	case TypeRwalk:
		return &Rwalk{}, true
	case TypeTread:
		return &Tread{}, true
	case TypeRread:
		return &Rread{}, true
	case TypeTwrite:
		return &Twrite{}, true
	case TypeRwrite:
		return &Rwrite{}, true
	case TypeTclunk:
		return &Tclunk{}, true
	case TypeRclunk:
		return &Rclunk{}, true
	case TypeTremove:
		return &Tremove{}, true
	case TypeRremove:
		return &Rremove{}, true
	default:
		return nil, false
	}
}

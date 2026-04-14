package p9l

import (
	"fmt"
	"io"

	"github.com/dotwaffle/ninep/internal/bufpool"
	"github.com/dotwaffle/ninep/proto"
)

// Encode writes a complete 9P2000.L message to w, including the size[4] +
// type[1] + tag[2] header followed by the message body.
//
// The body buffer is borrowed from proto/internal/bufpool and returned via
// defer. Passing the pooled *bytes.Buffer into msg.EncodeTo lets the
// proto.Write* helpers take their zero-alloc *bytes.Buffer fast path
// (established in plan 08-02). Put-after-Write is safe: w.Write(body.Bytes())
// returns synchronously — both bytes.Buffer and net.Conn copy input before
// returning, so the pooled buffer is no longer referenced when PutBuf runs.
func Encode(w io.Writer, tag proto.Tag, msg proto.Message) error {
	body := bufpool.GetBuf()
	defer bufpool.PutBuf(body)

	if err := msg.EncodeTo(body); err != nil {
		return fmt.Errorf("encode %s body: %w", msg.Type(), err)
	}

	size := uint32(proto.HeaderSize) + uint32(body.Len())

	if err := proto.WriteUint32(w, size); err != nil {
		return fmt.Errorf("encode size: %w", err)
	}
	if err := proto.WriteUint8(w, uint8(msg.Type())); err != nil {
		return fmt.Errorf("encode type: %w", err)
	}
	if err := proto.WriteUint16(w, uint16(tag)); err != nil {
		return fmt.Errorf("encode tag: %w", err)
	}
	if _, err := w.Write(body.Bytes()); err != nil {
		return fmt.Errorf("encode body: %w", err)
	}
	return nil
}

// Decode reads a complete 9P2000.L message from r, parsing the size[4] +
// type[1] + tag[2] header and dispatching to the correct message struct for
// body decoding. The body is read through an io.LimitReader bounded to the
// declared message size.
func Decode(r io.Reader) (proto.Tag, proto.Message, error) {
	size, err := proto.ReadUint32(r)
	if err != nil {
		return 0, nil, fmt.Errorf("decode size: %w", err)
	}
	if size < uint32(proto.HeaderSize) {
		return 0, nil, fmt.Errorf("message size %d too small (minimum %d)", size, proto.HeaderSize)
	}

	msgType, err := proto.ReadUint8(r)
	if err != nil {
		return 0, nil, fmt.Errorf("decode type: %w", err)
	}

	tag, err := proto.ReadUint16(r)
	if err != nil {
		return 0, nil, fmt.Errorf("decode tag: %w", err)
	}

	msg, err := newMessage(proto.MessageType(msgType))
	if err != nil {
		return 0, nil, err
	}

	bodySize := int64(size) - int64(proto.HeaderSize)
	bodyReader := io.LimitReader(r, bodySize)

	if err := msg.DecodeFrom(bodyReader); err != nil {
		return 0, nil, fmt.Errorf("decode %s body: %w", msg.Type(), err)
	}

	return proto.Tag(tag), msg, nil
}

// newMessage returns a pointer to a zero-value struct for the given message
// type. It handles all 9P2000.L-specific types and shared base types.
func newMessage(t proto.MessageType) (proto.Message, error) {
	switch t {
	// 9P2000.L-specific message types.
	case proto.TypeRlerror:
		return &Rlerror{}, nil
	case proto.TypeTstatfs:
		return &Tstatfs{}, nil
	case proto.TypeRstatfs:
		return &Rstatfs{}, nil
	case proto.TypeTlopen:
		return &Tlopen{}, nil
	case proto.TypeRlopen:
		return &Rlopen{}, nil
	case proto.TypeTlcreate:
		return &Tlcreate{}, nil
	case proto.TypeRlcreate:
		return &Rlcreate{}, nil
	case proto.TypeTsymlink:
		return &Tsymlink{}, nil
	case proto.TypeRsymlink:
		return &Rsymlink{}, nil
	case proto.TypeTmknod:
		return &Tmknod{}, nil
	case proto.TypeRmknod:
		return &Rmknod{}, nil
	case proto.TypeTrename:
		return &Trename{}, nil
	case proto.TypeRrename:
		return &Rrename{}, nil
	case proto.TypeTreadlink:
		return &Treadlink{}, nil
	case proto.TypeRreadlink:
		return &Rreadlink{}, nil
	case proto.TypeTgetattr:
		return &Tgetattr{}, nil
	case proto.TypeRgetattr:
		return &Rgetattr{}, nil
	case proto.TypeTsetattr:
		return &Tsetattr{}, nil
	case proto.TypeRsetattr:
		return &Rsetattr{}, nil
	case proto.TypeTxattrwalk:
		return &Txattrwalk{}, nil
	case proto.TypeRxattrwalk:
		return &Rxattrwalk{}, nil
	case proto.TypeTxattrcreate:
		return &Txattrcreate{}, nil
	case proto.TypeRxattrcreate:
		return &Rxattrcreate{}, nil
	case proto.TypeTreaddir:
		return &Treaddir{}, nil
	case proto.TypeRreaddir:
		return &Rreaddir{}, nil
	case proto.TypeTfsync:
		return &Tfsync{}, nil
	case proto.TypeRfsync:
		return &Rfsync{}, nil
	case proto.TypeTlock:
		return &Tlock{}, nil
	case proto.TypeRlock:
		return &Rlock{}, nil
	case proto.TypeTgetlock:
		return &Tgetlock{}, nil
	case proto.TypeRgetlock:
		return &Rgetlock{}, nil
	case proto.TypeTlink:
		return &Tlink{}, nil
	case proto.TypeRlink:
		return &Rlink{}, nil
	case proto.TypeTmkdir:
		return &Tmkdir{}, nil
	case proto.TypeRmkdir:
		return &Rmkdir{}, nil
	case proto.TypeTrenameat:
		return &Trenameat{}, nil
	case proto.TypeRrenameat:
		return &Rrenameat{}, nil
	case proto.TypeTunlinkat:
		return &Tunlinkat{}, nil
	case proto.TypeRunlinkat:
		return &Runlinkat{}, nil

	// Shared base message types.
	case proto.TypeTversion:
		return &proto.Tversion{}, nil
	case proto.TypeRversion:
		return &proto.Rversion{}, nil
	case proto.TypeTauth:
		return &proto.Tauth{}, nil
	case proto.TypeRauth:
		return &proto.Rauth{}, nil
	case proto.TypeTattach:
		return &proto.Tattach{}, nil
	case proto.TypeRattach:
		return &proto.Rattach{}, nil
	case proto.TypeTflush:
		return &proto.Tflush{}, nil
	case proto.TypeRflush:
		return &proto.Rflush{}, nil
	case proto.TypeTwalk:
		return &proto.Twalk{}, nil
	case proto.TypeRwalk:
		return &proto.Rwalk{}, nil
	case proto.TypeTread:
		return &proto.Tread{}, nil
	case proto.TypeRread:
		return &proto.Rread{}, nil
	case proto.TypeTwrite:
		return &proto.Twrite{}, nil
	case proto.TypeRwrite:
		return &proto.Rwrite{}, nil
	case proto.TypeTclunk:
		return &proto.Tclunk{}, nil
	case proto.TypeRclunk:
		return &proto.Rclunk{}, nil
	case proto.TypeTremove:
		return &proto.Tremove{}, nil
	case proto.TypeRremove:
		return &proto.Rremove{}, nil

	default:
		return nil, fmt.Errorf("unknown message type %d", t)
	}
}

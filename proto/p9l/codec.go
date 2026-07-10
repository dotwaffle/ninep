package p9l

import (
	"fmt"
	"io"

	"github.com/dotwaffle/ninep/proto"
)

// Encode writes a complete 9P2000.L message to w, including the size[4] +
// type[1] + tag[2] header followed by the message body.
func Encode(w io.Writer, tag proto.Tag, msg proto.Message) error {
	return proto.EncodeFrame(w, tag, msg)
}

// Decode reads a complete 9P2000.L message from r, parsing the size[4] +
// type[1] + tag[2] header and dispatching to the correct message struct for
// body decoding.
func Decode(r io.Reader) (proto.Tag, proto.Message, error) {
	return proto.DecodeFrame(r, newMessage)
}

// newMessage returns a pointer to a zero-value struct for the given message
// type. It handles 9P2000.L-specific types, falling back to
// proto.NewBaseMessage for types shared with 9P2000.u.
func newMessage(t proto.MessageType) (proto.Message, error) {
	switch t {
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
	default:
		if m, ok := proto.NewBaseMessage(t); ok {
			return m, nil
		}
		return nil, fmt.Errorf("unknown message type %d", t)
	}
}

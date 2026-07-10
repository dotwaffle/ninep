package p9u

import (
	"fmt"
	"io"

	"github.com/dotwaffle/ninep/proto"
)

// Encode writes a complete 9P2000.u message to w, including the size[4] +
// type[1] + tag[2] header followed by the message body.
func Encode(w io.Writer, tag proto.Tag, msg proto.Message) error {
	return proto.EncodeFrame(w, tag, msg)
}

// Decode reads a complete 9P2000.u message from r, parsing the size[4] +
// type[1] + tag[2] header and dispatching to the correct message struct for
// body decoding.
func Decode(r io.Reader) (proto.Tag, proto.Message, error) {
	return proto.DecodeFrame(r, newMessage)
}

// newMessage returns a pointer to a zero-value struct for the given message
// type. It handles 9P2000.u-specific types, falling back to
// proto.NewBaseMessage for types shared with 9P2000.L.
func newMessage(t proto.MessageType) (proto.Message, error) {
	switch t {
	case proto.TypeRerror:
		return &Rerror{}, nil
	case proto.TypeTopen:
		return &Topen{}, nil
	case proto.TypeRopen:
		return &Ropen{}, nil
	case proto.TypeTcreate:
		return &Tcreate{}, nil
	case proto.TypeRcreate:
		return &Rcreate{}, nil
	case proto.TypeTstat:
		return &Tstat{}, nil
	case proto.TypeRstat:
		return &Rstat{}, nil
	case proto.TypeTwstat:
		return &Twstat{}, nil
	case proto.TypeRwstat:
		return &Rwstat{}, nil
	default:
		if m, ok := proto.NewBaseMessage(t); ok {
			return m, nil
		}
		return nil, fmt.Errorf("unknown message type %d", t)
	}
}

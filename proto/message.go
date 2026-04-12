// Package proto defines the shared types, constants, and encoding helpers for
// the 9P2000.L and 9P2000.u wire protocols.
package proto

import (
	"fmt"
	"io"
)

// MessageType identifies the 9P message type byte on the wire.
type MessageType uint8

// 9P2000.L-specific message types.
const (
	TypeTlerror     MessageType = 6
	TypeRlerror     MessageType = 7
	TypeTstatfs     MessageType = 8
	TypeRstatfs     MessageType = 9
	TypeTlopen      MessageType = 12
	TypeRlopen      MessageType = 13
	TypeTlcreate    MessageType = 14
	TypeRlcreate    MessageType = 15
	TypeTsymlink    MessageType = 16
	TypeRsymlink    MessageType = 17
	TypeTmknod      MessageType = 18
	TypeRmknod      MessageType = 19
	TypeTrename     MessageType = 20
	TypeRrename     MessageType = 21
	TypeTreadlink   MessageType = 22
	TypeRreadlink   MessageType = 23
	TypeTgetattr    MessageType = 24
	TypeRgetattr    MessageType = 25
	TypeTsetattr    MessageType = 26
	TypeRsetattr    MessageType = 27
	TypeTxattrwalk  MessageType = 30
	TypeRxattrwalk  MessageType = 31
	TypeTxattrcreate MessageType = 32
	TypeRxattrcreate MessageType = 33
	TypeTreaddir    MessageType = 40
	TypeRreaddir    MessageType = 41
	TypeTfsync      MessageType = 50
	TypeRfsync      MessageType = 51
	TypeTlock       MessageType = 52
	TypeRlock       MessageType = 53
	TypeTgetlock    MessageType = 54
	TypeRgetlock    MessageType = 55
	TypeTlink       MessageType = 70
	TypeRlink       MessageType = 71
	TypeTmkdir      MessageType = 72
	TypeRmkdir      MessageType = 73
	TypeTrenameat   MessageType = 74
	TypeRrenameat   MessageType = 75
	TypeTunlinkat   MessageType = 76
	TypeRunlinkat   MessageType = 77
)

// Shared base message types used by both 9P2000.L and 9P2000.u.
const (
	TypeTversion MessageType = 100
	TypeRversion MessageType = 101
	TypeTauth    MessageType = 102
	TypeRauth    MessageType = 103
	TypeTattach  MessageType = 104
	TypeRattach  MessageType = 105
	TypeTerror   MessageType = 106 // Never sent on the wire.
	TypeRerror   MessageType = 107
	TypeTflush   MessageType = 108
	TypeRflush   MessageType = 109
	TypeTwalk    MessageType = 110
	TypeRwalk    MessageType = 111
	TypeTopen    MessageType = 112
	TypeRopen    MessageType = 113
	TypeTcreate  MessageType = 114
	TypeRcreate  MessageType = 115
	TypeTread    MessageType = 116
	TypeRread    MessageType = 117
	TypeTwrite   MessageType = 118
	TypeRwrite   MessageType = 119
	TypeTclunk   MessageType = 120
	TypeRclunk   MessageType = 121
	TypeTremove  MessageType = 122
	TypeRremove  MessageType = 123
	TypeTstat    MessageType = 124
	TypeRstat    MessageType = 125
	TypeTwstat   MessageType = 126
	TypeRwstat   MessageType = 127
)

// messageTypeNames maps message types to human-readable names.
var messageTypeNames = map[MessageType]string{
	TypeTlerror:      "Tlerror",
	TypeRlerror:      "Rlerror",
	TypeTstatfs:      "Tstatfs",
	TypeRstatfs:      "Rstatfs",
	TypeTlopen:       "Tlopen",
	TypeRlopen:       "Rlopen",
	TypeTlcreate:     "Tlcreate",
	TypeRlcreate:     "Rlcreate",
	TypeTsymlink:     "Tsymlink",
	TypeRsymlink:     "Rsymlink",
	TypeTmknod:       "Tmknod",
	TypeRmknod:       "Rmknod",
	TypeTrename:      "Trename",
	TypeRrename:      "Rrename",
	TypeTreadlink:    "Treadlink",
	TypeRreadlink:    "Rreadlink",
	TypeTgetattr:     "Tgetattr",
	TypeRgetattr:     "Rgetattr",
	TypeTsetattr:     "Tsetattr",
	TypeRsetattr:     "Rsetattr",
	TypeTxattrwalk:   "Txattrwalk",
	TypeRxattrwalk:   "Rxattrwalk",
	TypeTxattrcreate: "Txattrcreate",
	TypeRxattrcreate: "Rxattrcreate",
	TypeTreaddir:     "Treaddir",
	TypeRreaddir:     "Rreaddir",
	TypeTfsync:       "Tfsync",
	TypeRfsync:       "Rfsync",
	TypeTlock:        "Tlock",
	TypeRlock:        "Rlock",
	TypeTgetlock:     "Tgetlock",
	TypeRgetlock:     "Rgetlock",
	TypeTlink:        "Tlink",
	TypeRlink:        "Rlink",
	TypeTmkdir:       "Tmkdir",
	TypeRmkdir:       "Rmkdir",
	TypeTrenameat:    "Trenameat",
	TypeRrenameat:    "Rrenameat",
	TypeTunlinkat:    "Tunlinkat",
	TypeRunlinkat:    "Runlinkat",
	TypeTversion:     "Tversion",
	TypeRversion:     "Rversion",
	TypeTauth:        "Tauth",
	TypeRauth:        "Rauth",
	TypeTattach:      "Tattach",
	TypeRattach:      "Rattach",
	TypeTerror:       "Terror",
	TypeRerror:       "Rerror",
	TypeTflush:       "Tflush",
	TypeRflush:       "Rflush",
	TypeTwalk:        "Twalk",
	TypeRwalk:        "Rwalk",
	TypeTopen:        "Topen",
	TypeRopen:        "Ropen",
	TypeTcreate:      "Tcreate",
	TypeRcreate:      "Rcreate",
	TypeTread:        "Tread",
	TypeRread:        "Rread",
	TypeTwrite:       "Twrite",
	TypeRwrite:       "Rwrite",
	TypeTclunk:       "Tclunk",
	TypeRclunk:       "Rclunk",
	TypeTremove:      "Tremove",
	TypeRremove:      "Rremove",
	TypeTstat:        "Tstat",
	TypeRstat:        "Rstat",
	TypeTwstat:       "Twstat",
	TypeRwstat:       "Rwstat",
}

// String returns the human-readable name of the message type.
func (t MessageType) String() string {
	if name, ok := messageTypeNames[t]; ok {
		return name
	}
	return fmt.Sprintf("MessageType(%d)", t)
}

// Message is implemented by all 9P message types. The methods encode and decode
// the message body only, excluding the size[4]+type[1]+tag[2] header which is
// handled at the transport layer.
type Message interface {
	// Type returns the protocol message type byte.
	Type() MessageType

	// EncodeTo writes the message body to w in 9P wire format.
	EncodeTo(w io.Writer) error

	// DecodeFrom reads the message body from r in 9P wire format.
	DecodeFrom(r io.Reader) error
}

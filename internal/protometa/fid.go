// Package protometa provides shared metadata extraction from protocol messages.
package protometa

import (
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
	"github.com/dotwaffle/ninep/proto/p9u"
)

// Fid extracts the primary fid from a request message.
func Fid(msg proto.Message) (proto.Fid, bool) {
	switch m := msg.(type) {
	case *proto.Tattach:
		return m.Fid, true
	case *proto.Twalk:
		return m.Fid, true
	case *proto.Tclunk:
		return m.Fid, true
	case *proto.Tread:
		return m.Fid, true
	case *proto.Twrite:
		return m.Fid, true
	case *proto.Tremove:
		return m.Fid, true
	case *p9l.Tlopen:
		return m.Fid, true
	case *p9l.Tgetattr:
		return m.Fid, true
	case *p9l.Tsetattr:
		return m.Fid, true
	case *p9l.Treaddir:
		return m.Fid, true
	case *p9l.Tlcreate:
		return m.Fid, true
	case *p9l.Tmkdir:
		return m.DirFid, true
	case *p9l.Tsymlink:
		return m.DirFid, true
	case *p9l.Tlink:
		return m.DirFid, true
	case *p9l.Tmknod:
		return m.DirFid, true
	case *p9l.Treadlink:
		return m.Fid, true
	case *p9l.Tstatfs:
		return m.Fid, true
	case *p9l.Tfsync:
		return m.Fid, true
	case *p9l.Tunlinkat:
		return m.DirFid, true
	case *p9l.Trenameat:
		return m.OldDirFid, true
	case *p9l.Trename:
		return m.Fid, true
	case *p9l.Tlock:
		return m.Fid, true
	case *p9l.Tgetlock:
		return m.Fid, true
	case *p9l.Txattrwalk:
		return m.Fid, true
	case *p9l.Txattrcreate:
		return m.Fid, true
	case *p9u.Topen:
		return m.Fid, true
	case *p9u.Tcreate:
		return m.Fid, true
	case *p9u.Tstat:
		return m.Fid, true
	case *p9u.Twstat:
		return m.Fid, true
	default:
		return 0, false
	}
}

//go:build freebsd

package passthrough

import (
	"context"

	"golang.org/x/sys/unix"

	"github.com/dotwaffle/ninep/proto"
)

// StatFS returns filesystem statistics for the filesystem containing this node.
//
// FreeBSD's Statfs_t uses Namemax (uint32) instead of Namelen (int64), Bavail
// is signed (int64), and Ffree is signed (int64); cast accordingly. Type is
// already uint32 on FreeBSD.
func (n *Node) StatFS(_ context.Context) (proto.FSStat, error) {
	var st unix.Statfs_t
	if err := unix.Fstatfs(n.fd, &st); err != nil {
		return proto.FSStat{}, toProtoErr(err)
	}
	return proto.FSStat{
		Type:    st.Type,
		BSize:   uint32(st.Bsize),
		Blocks:  st.Blocks,
		BFree:   st.Bfree,
		BAvail:  uint64(st.Bavail),
		Files:   st.Files,
		FFree:   uint64(st.Ffree),
		FSID:    uint64(uint32(st.Fsid.Val[0])) | uint64(uint32(st.Fsid.Val[1]))<<32,
		NameLen: st.Namemax,
	}, nil
}

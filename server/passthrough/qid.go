//go:build linux || freebsd

package passthrough

import (
	"golang.org/x/sys/unix"

	"github.com/dotwaffle/ninep/proto"
)

type fileIdentity struct {
	device uint64
	inode  uint64
}

func (r *Root) qidFor(st *unix.Stat_t) proto.QID {
	qid := statToQID(st)
	identity := fileIdentity{device: uint64(st.Dev), inode: st.Ino}

	r.qidMu.Lock()
	path, ok := r.qidPaths[identity]
	if !ok {
		path = r.nextQIDPath
		r.nextQIDPath++
		r.qidPaths[identity] = path
	}
	r.qidMu.Unlock()

	qid.Path = path
	return qid
}

func qidVersion(seconds, nanoseconds int64) uint32 {
	return uint32(uint64(seconds)*1_000_000_000 + uint64(nanoseconds))
}

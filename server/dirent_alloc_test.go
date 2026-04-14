package server

import (
	"strconv"
	"testing"

	"github.com/dotwaffle/ninep/proto"
)

// TestEncodeDirents_ZeroAllocs pins the post-08-03 allocation budget for
// EncodeDirents. With the internal bufpool + copy-out pattern, exactly one
// allocation remains per call (the returned []byte). The Phase 7 baseline
// was 4011 allocs/op at n=1000 — dominated by bytes.Buffer grow-doubling
// as the per-dirent field writes expanded the backing slice.
//
// The pool's pre-grown capacity (PoolMaxBufSize = 128KB) absorbs the entire
// pack loop without a single grow, and the proto.Write* fast path (plan
// 08-02) drives per-field allocs to zero. Only the copy-out slice remains.
func TestEncodeDirents_ZeroAllocs(t *testing.T) {
	dirents := make([]proto.Dirent, 100)
	for i := range dirents {
		dirents[i] = proto.Dirent{
			QID:    proto.QID{Type: proto.QTFILE, Version: 1, Path: uint64(i)},
			Offset: uint64(i + 1),
			Type:   uint8(proto.QTFILE),
			Name:   "file" + strconv.Itoa(i),
		}
	}

	// Warm the pool — first-use path takes the pool's New func.
	for range 10 {
		_, _ = EncodeDirents(dirents, 64*1024)
	}

	allocs := testing.AllocsPerRun(100, func() {
		_, _ = EncodeDirents(dirents, 64*1024)
	})

	// Expect exactly 1 alloc per call (the copy-out slice). Allow a small
	// slack of 1 to absorb runtime noise; 2 would be a regression worth
	// investigating.
	if allocs > 1 {
		t.Errorf("EncodeDirents allocs/op: got %v, want <= 1 (copy-out only)", allocs)
	}
}

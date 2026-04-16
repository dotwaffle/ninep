package server

import (
	"github.com/dotwaffle/ninep/internal/bufpool"
	"github.com/dotwaffle/ninep/proto"
)

// EncodeDirents packs dirents into bytes fitting within maxBytes.
// Returns the packed bytes and the number of entries that fit.
//
// Each entry is encoded as:
//
//	qid[13] + offset[8] + type[1] + name[s]
//
// where name[s] = len[2] + name_bytes.
//
// The returned []byte is a freshly-allocated copy-out — safe to retain past
// the call boundary. Internally, a *bytes.Buffer is borrowed from bufpool
// for the pack loop and returned to the pool via defer.
//
// Why copy-out instead of returning the pooled buffer directly: the
// pooled *bytes.Buffer returns to bufpool via the deferred PutBuf at the
// end of this function, which executes before the caller encodes the
// response body via msg.EncodeTo inside sendResponseInline. If we handed
// back the pooled buffer's bytes directly, the response encoder would be
// reading a slice that aliases a buffer already returned to (and possibly
// reissued from) the pool. The copy-out costs exactly one allocation per
// call (the output slice) and preserves safety. See 08-CONTEXT.md
// "EncodeDirents Signature (REVISED 2026-04-14 — rolled back)".
//
// Because buf is a concrete *bytes.Buffer, the proto.Write* helpers take
// their zero-alloc fast path (plan 08-02): per-field encoding adds no
// allocations. Net effect: BenchmarkEncodeDirents/n=1000 goes from 4011
// allocs/op (Phase 7 baseline, dominated by bytes.Buffer grow-doubling)
// to 1 alloc/op (the copy-out slice).
func EncodeDirents(dirents []proto.Dirent, maxBytes uint32) ([]byte, int) {
	if len(dirents) == 0 {
		return nil, 0
	}

	buf := bufpool.GetBuf()
	defer bufpool.PutBuf(buf)
	count := 0

	for _, d := range dirents {
		entrySize := proto.QIDSize + 8 + 1 + 2 + len(d.Name)
		if buf.Len()+entrySize > int(maxBytes) {
			break
		}

		// All proto.Write* functions write to bytes.Buffer, which
		// never returns write errors. The concrete *bytes.Buffer
		// triggers the zero-alloc fast path from plan 08-02.
		_ = proto.WriteQID(buf, d.QID)
		_ = proto.WriteUint64(buf, d.Offset)
		_ = proto.WriteUint8(buf, d.Type)
		_ = proto.WriteString(buf, d.Name)
		count++
	}

	// Copy-out — the pooled buffer returns to the pool via defer AFTER
	// this function returns, at which point the caller holds only the
	// fresh `out` slice. No aliasing; safe even though the response
	// encoder runs later than this PutBuf.
	out := make([]byte, buf.Len())
	copy(out, buf.Bytes())
	return out, count
}

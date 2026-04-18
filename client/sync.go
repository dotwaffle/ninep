package client

import (
	"context"
	"fmt"

	"github.com/dotwaffle/ninep/proto"
)

// syncImpl refreshes this File's cached size from the server. On
// 9P2000.L it issues Tgetattr(fid, AttrSize); on 9P2000.u it issues
// Tstat(fid). On success, f.cachedSize is updated under f.mu so a
// concurrent [File.Seek] with [io.SeekEnd] observes the fresh value.
//
// syncImpl replaces the Phase 20 syncStub (client/sync_stub.go), which
// returned nil unconditionally without any wire op. The stub's
// documented contract was "callers that depend on [File.Seek] with
// [io.SeekEnd] treat Sync as 'no effect'"; Phase 21 closes that gap
// without an API change — callers never saw the stub.
//
// Error handling: on failure, f.cachedSize is NOT modified — the
// previous value is preserved rather than zeroed. This keeps a
// successful prior Sync's size stable across a transient error.
//
// Context source: syncImpl uses a bounded background context
// ([cleanupDeadline]) rather than accepting a caller-supplied ctx.
// Rationale: [File.Sync] mirrors the fsync(2) shape (no ctx), and a
// wedged server must not hang the caller indefinitely. Callers that
// want caller-controlled cancellation use [File.Stat] (takes a ctx;
// returns size via the returned Stat without mutating f.cachedSize).
func (f *File) syncImpl() error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupDeadline)
	defer cancel()
	var size int64
	switch f.conn.dialect {
	case protocolL:
		attr, err := f.conn.Raw().Tgetattr(ctx, f.fid, proto.AttrSize)
		if err != nil {
			return err
		}
		size = int64(attr.Size)
	case protocolU:
		stat, err := f.conn.Raw().Tstat(ctx, f.fid)
		if err != nil {
			return err
		}
		size = int64(stat.Length)
	default:
		return fmt.Errorf("%w: %v", ErrDialectInvariant, f.conn.dialect)
	}
	f.mu.Lock()
	f.cachedSize = size
	f.mu.Unlock()
	return nil
}

package client

import (
	"context"
	"fmt"

	"github.com/dotwaffle/ninep/proto"
)

// RefreshSize refreshes this File's cached size from the server by
// issuing Tgetattr(fid, AttrSize) on 9P2000.L or Tstat(fid) on
// 9P2000.u. On success, f.cachedSize is updated under f.mu so a
// concurrent [File.Seek] with [io.SeekEnd] observes the fresh value.
// Callers that use SeekEnd on a file whose size may have changed
// server-side (concurrent writers, a truncate via another fid) call
// RefreshSize first so the subsequent SeekEnd returns a current value.
//
// Every call issues a fresh wire op; there is no staleness check.
// RefreshSize does NOT flush data to durable storage -- that is
// [File.Fsync].
//
// Error handling: on failure, f.cachedSize is NOT modified - the
// previous value is preserved rather than zeroed. This keeps a
// successful prior refresh's size stable across a transient error.
//
// Context source: RefreshSize uses a bounded background context
// ([cleanupDeadline]) rather than accepting a caller-supplied ctx, so
// a wedged server cannot hang the caller indefinitely. Callers that
// want caller-controlled cancellation use [File.Stat] (takes a ctx;
// returns size via the returned FileInfo without mutating
// f.cachedSize).
func (f *File) RefreshSize() error {
	if err := f.beginOp(); err != nil {
		return err
	}
	defer f.endOp()
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

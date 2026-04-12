package server

import (
	"context"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
)

// handleLopen dispatches to NodeOpener, transitions fid to opened state, and
// stores the returned FileHandle.
func (c *conn) handleLopen(ctx context.Context, m *p9l.Tlopen) proto.Message {
	fs := c.fids.get(m.Fid)
	if fs == nil {
		return c.errorMsg(proto.EBADF)
	}
	if fs.state != fidAllocated {
		return c.errorMsg(proto.EBADF)
	}

	opener, ok := fs.node.(NodeOpener)
	if !ok {
		return c.errorMsg(proto.ENOSYS)
	}

	handle, flags, err := opener.Open(ctx, m.Flags)
	if err != nil {
		return c.errorMsg(errnoFromError(err))
	}

	qid := nodeQID(fs.node)

	if !c.fids.markOpenedWithHandle(m.Fid, handle) {
		// Raced with another open or clunk; should not happen in practice.
		return c.errorMsg(proto.EBADF)
	}

	// IOUnit: max data that fits in one Rread response.
	// 4 bytes for the Rread data count prefix.
	iounit := c.msize - proto.HeaderSize - 4

	_ = flags // Response flags from Open are passed through in IOUnit field position.
	return &p9l.Rlopen{QID: qid, IOUnit: iounit}
}

// handleRead dispatches to FileReader (handle-first) then NodeReader (fallback).
func (c *conn) handleRead(ctx context.Context, m *proto.Tread) proto.Message {
	fs := c.fids.get(m.Fid)
	if fs == nil {
		return c.errorMsg(proto.EBADF)
	}
	if fs.state != fidOpened {
		return c.errorMsg(proto.EBADF)
	}

	// Clamp count to prevent oversized allocations (T-03-03).
	maxData := c.msize - proto.HeaderSize - 4
	if m.Count > maxData {
		m.Count = maxData
	}

	// FileHandle dispatch first (per API-04).
	if fs.handle != nil {
		if reader, ok := fs.handle.(FileReader); ok {
			data, err := reader.Read(ctx, m.Offset, m.Count)
			if err != nil {
				return c.errorMsg(errnoFromError(err))
			}
			return &proto.Rread{Data: data}
		}
	}

	// Node fallback.
	reader, ok := fs.node.(NodeReader)
	if !ok {
		return c.errorMsg(proto.ENOSYS)
	}

	data, err := reader.Read(ctx, m.Offset, m.Count)
	if err != nil {
		return c.errorMsg(errnoFromError(err))
	}
	return &proto.Rread{Data: data}
}

// handleWrite dispatches to FileWriter (handle-first) then NodeWriter (fallback).
func (c *conn) handleWrite(ctx context.Context, m *proto.Twrite) proto.Message {
	fs := c.fids.get(m.Fid)
	if fs == nil {
		return c.errorMsg(proto.EBADF)
	}
	if fs.state != fidOpened {
		return c.errorMsg(proto.EBADF)
	}

	// FileHandle dispatch first (per API-04).
	if fs.handle != nil {
		if writer, ok := fs.handle.(FileWriter); ok {
			count, err := writer.Write(ctx, m.Data, m.Offset)
			if err != nil {
				return c.errorMsg(errnoFromError(err))
			}
			return &proto.Rwrite{Count: count}
		}
	}

	// Node fallback.
	writer, ok := fs.node.(NodeWriter)
	if !ok {
		return c.errorMsg(proto.ENOSYS)
	}

	count, err := writer.Write(ctx, m.Data, m.Offset)
	if err != nil {
		return c.errorMsg(errnoFromError(err))
	}
	return &proto.Rwrite{Count: count}
}

// handleGetattr dispatches to NodeGetattrer.
func (c *conn) handleGetattr(ctx context.Context, m *p9l.Tgetattr) proto.Message {
	fs := c.fids.get(m.Fid)
	if fs == nil {
		return c.errorMsg(proto.EBADF)
	}

	getter, ok := fs.node.(NodeGetattrer)
	if !ok {
		return c.errorMsg(proto.ENOSYS)
	}

	attr, err := getter.Getattr(ctx, m.RequestMask)
	if err != nil {
		return c.errorMsg(errnoFromError(err))
	}

	// Override QID from server's authoritative source (T-03-09).
	attr.QID = nodeQID(fs.node)

	return &p9l.Rgetattr{Attr: attr}
}

// handleSetattr dispatches to NodeSetattrer.
func (c *conn) handleSetattr(ctx context.Context, m *p9l.Tsetattr) proto.Message {
	fs := c.fids.get(m.Fid)
	if fs == nil {
		return c.errorMsg(proto.EBADF)
	}

	setter, ok := fs.node.(NodeSetattrer)
	if !ok {
		return c.errorMsg(proto.ENOSYS)
	}

	if err := setter.Setattr(ctx, m.Attr); err != nil {
		return c.errorMsg(errnoFromError(err))
	}

	return &p9l.Rsetattr{}
}

// handleReaddir dispatches to raw or simple readdir interfaces with
// handle-first, node-fallback priority.
func (c *conn) handleReaddir(ctx context.Context, m *p9l.Treaddir) proto.Message {
	fs := c.fids.get(m.Fid)
	if fs == nil {
		return c.errorMsg(proto.EBADF)
	}
	if fs.state != fidOpened {
		return c.errorMsg(proto.EBADF)
	}

	// Clamp count to prevent oversized allocations (T-03-03).
	maxData := c.msize - proto.HeaderSize - 4
	if m.Count > maxData {
		m.Count = maxData
	}

	// FileHandle dispatch chain (priority order).
	if fs.handle != nil {
		if raw, ok := fs.handle.(FileRawReaddirer); ok {
			data, err := raw.RawReaddir(ctx, m.Offset, m.Count)
			if err != nil {
				return c.errorMsg(errnoFromError(err))
			}
			return &p9l.Rreaddir{Data: data}
		}
		if rd, ok := fs.handle.(FileReaddirer); ok {
			return c.readdirSimple(ctx, fs, m, rd)
		}
	}

	// Node dispatch chain (fallback).
	if raw, ok := fs.node.(NodeRawReaddirer); ok {
		data, err := raw.RawReaddir(ctx, m.Offset, m.Count)
		if err != nil {
			return c.errorMsg(errnoFromError(err))
		}
		return &p9l.Rreaddir{Data: data}
	}
	if rd, ok := fs.node.(NodeReaddirer); ok {
		return c.readdirSimple(ctx, fs, m, rd)
	}

	return c.errorMsg(proto.ENOSYS)
}

// readdirProvider abstracts FileReaddirer and NodeReaddirer for the simple
// caching strategy.
type readdirProvider interface {
	Readdir(ctx context.Context) ([]proto.Dirent, error)
}

// readdirSimple implements the server-managed dirent caching strategy for
// simple Readdirer implementations. Dirents are fetched once and cached on the
// fid. Offset 0 re-fetches.
func (c *conn) readdirSimple(ctx context.Context, fs *fidState, m *p9l.Treaddir, rd readdirProvider) proto.Message {
	// Re-fetch on offset 0 (client is re-reading from start) or first call.
	if m.Offset == 0 || !fs.dirCached {
		dirents, err := rd.Readdir(ctx)
		if err != nil {
			return c.errorMsg(errnoFromError(err))
		}
		// Assign offset cookies: 1-based index so offset 0 means "start".
		for i := range dirents {
			dirents[i].Offset = uint64(i + 1)
		}
		fs.dirCache = dirents
		fs.dirCached = true
	}

	// Find starting entry. Offset N means "entries after the one with cookie N",
	// so start from index N (since cookie = index+1).
	start := int(m.Offset)
	if start > len(fs.dirCache) {
		start = len(fs.dirCache)
	}

	remaining := fs.dirCache[start:]
	data, _ := EncodeDirents(remaining, m.Count)
	return &p9l.Rreaddir{Data: data}
}

// handleLcreate dispatches to NodeCreater, mutates fid to the new child in
// opened state per 9P spec.
func (c *conn) handleLcreate(ctx context.Context, m *p9l.Tlcreate) proto.Message {
	fs := c.fids.get(m.Fid)
	if fs == nil {
		return c.errorMsg(proto.EBADF)
	}

	if !validName(m.Name) {
		return c.errorMsg(proto.EINVAL)
	}

	creator, ok := fs.node.(NodeCreater)
	if !ok {
		return c.errorMsg(proto.ENOSYS)
	}

	child, handle, _, err := creator.Create(ctx, m.Name, m.Flags, m.Mode, m.GID)
	if err != nil {
		return c.errorMsg(errnoFromError(err))
	}

	// Per 9P spec: Tlcreate creates AND opens. The fid mutates to the new child.
	// update changes the node but preserves fidAllocated state.
	c.fids.update(m.Fid, child)
	// markOpenedWithHandle transitions to fidOpened and stores handle.
	c.fids.markOpenedWithHandle(m.Fid, handle)

	// Register child in parent Inode tree if both implement InodeEmbedder.
	if parentIE, ok := fs.node.(InodeEmbedder); ok {
		if childIE, ok := child.(InodeEmbedder); ok {
			parentIE.EmbeddedInode().AddChild(m.Name, childIE.EmbeddedInode())
		}
	}

	qid := nodeQID(child)
	iounit := c.msize - proto.HeaderSize - 4

	return &p9l.Rlcreate{QID: qid, IOUnit: iounit}
}

// handleMkdir dispatches to NodeMkdirer. The dirfid is NOT mutated (unlike Tlcreate).
func (c *conn) handleMkdir(ctx context.Context, m *p9l.Tmkdir) proto.Message {
	fs := c.fids.get(m.DirFid)
	if fs == nil {
		return c.errorMsg(proto.EBADF)
	}

	if !validName(m.Name) {
		return c.errorMsg(proto.EINVAL)
	}

	mkdirer, ok := fs.node.(NodeMkdirer)
	if !ok {
		return c.errorMsg(proto.ENOSYS)
	}

	child, err := mkdirer.Mkdir(ctx, m.Name, m.Mode, m.GID)
	if err != nil {
		return c.errorMsg(errnoFromError(err))
	}

	// Register child in parent Inode tree if both implement InodeEmbedder.
	if parentIE, ok := fs.node.(InodeEmbedder); ok {
		if childIE, ok := child.(InodeEmbedder); ok {
			parentIE.EmbeddedInode().AddChild(m.Name, childIE.EmbeddedInode())
		}
	}

	return &p9l.Rmkdir{QID: nodeQID(child)}
}

// validName returns true if name is a valid 9P file name (no /, no NUL,
// non-empty, not "." or "..").
func validName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for _, b := range []byte(name) {
		if b == '/' || b == 0 {
			return false
		}
	}
	return true
}

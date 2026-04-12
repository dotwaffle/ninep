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

// handleSymlink dispatches to NodeSymlinker and registers the new child in the
// Inode tree.
func (c *conn) handleSymlink(ctx context.Context, m *p9l.Tsymlink) proto.Message {
	fs := c.fids.get(m.DirFid)
	if fs == nil {
		return c.errorMsg(proto.EBADF)
	}

	if !validName(m.Name) {
		return c.errorMsg(proto.EINVAL)
	}

	symlinker, ok := fs.node.(NodeSymlinker)
	if !ok {
		return c.errorMsg(proto.ENOSYS)
	}

	child, err := symlinker.Symlink(ctx, m.Name, m.Target, m.GID)
	if err != nil {
		return c.errorMsg(errnoFromError(err))
	}

	// Register child in parent Inode tree if both implement InodeEmbedder.
	if parentIE, ok := fs.node.(InodeEmbedder); ok {
		if childIE, ok := child.(InodeEmbedder); ok {
			parentIE.EmbeddedInode().AddChild(m.Name, childIE.EmbeddedInode())
		}
	}

	return &p9l.Rsymlink{QID: nodeQID(child)}
}

// handleLink dispatches to NodeLinker. The directory (DirFid) receives the
// request; the target (Fid) is the existing node being linked.
func (c *conn) handleLink(ctx context.Context, m *p9l.Tlink) proto.Message {
	dirFS := c.fids.get(m.DirFid)
	if dirFS == nil {
		return c.errorMsg(proto.EBADF)
	}

	targetFS := c.fids.get(m.Fid)
	if targetFS == nil {
		return c.errorMsg(proto.EBADF)
	}

	if !validName(m.Name) {
		return c.errorMsg(proto.EINVAL)
	}

	linker, ok := dirFS.node.(NodeLinker)
	if !ok {
		return c.errorMsg(proto.ENOSYS)
	}

	if err := linker.Link(ctx, targetFS.node, m.Name); err != nil {
		return c.errorMsg(errnoFromError(err))
	}

	return &p9l.Rlink{}
}

// handleMknod dispatches to NodeMknoder and registers the new child in the
// Inode tree.
func (c *conn) handleMknod(ctx context.Context, m *p9l.Tmknod) proto.Message {
	fs := c.fids.get(m.DirFid)
	if fs == nil {
		return c.errorMsg(proto.EBADF)
	}

	if !validName(m.Name) {
		return c.errorMsg(proto.EINVAL)
	}

	mknoder, ok := fs.node.(NodeMknoder)
	if !ok {
		return c.errorMsg(proto.ENOSYS)
	}

	child, err := mknoder.Mknod(ctx, m.Name, m.Mode, m.Major, m.Minor, m.GID)
	if err != nil {
		return c.errorMsg(errnoFromError(err))
	}

	// Register child in parent Inode tree if both implement InodeEmbedder.
	if parentIE, ok := fs.node.(InodeEmbedder); ok {
		if childIE, ok := child.(InodeEmbedder); ok {
			parentIE.EmbeddedInode().AddChild(m.Name, childIE.EmbeddedInode())
		}
	}

	return &p9l.Rmknod{QID: nodeQID(child)}
}

// handleReadlink dispatches to NodeReadlinker.
func (c *conn) handleReadlink(ctx context.Context, m *p9l.Treadlink) proto.Message {
	fs := c.fids.get(m.Fid)
	if fs == nil {
		return c.errorMsg(proto.EBADF)
	}

	readlinker, ok := fs.node.(NodeReadlinker)
	if !ok {
		return c.errorMsg(proto.ENOSYS)
	}

	target, err := readlinker.Readlink(ctx)
	if err != nil {
		return c.errorMsg(errnoFromError(err))
	}

	return &p9l.Rreadlink{Target: target}
}

// handleStatfs dispatches to NodeStatFSer.
func (c *conn) handleStatfs(ctx context.Context, m *p9l.Tstatfs) proto.Message {
	fs := c.fids.get(m.Fid)
	if fs == nil {
		return c.errorMsg(proto.EBADF)
	}

	statfser, ok := fs.node.(NodeStatFSer)
	if !ok {
		return c.errorMsg(proto.ENOSYS)
	}

	stat, err := statfser.StatFS(ctx)
	if err != nil {
		return c.errorMsg(errnoFromError(err))
	}

	return &p9l.Rstatfs{Stat: stat}
}

// handleUnlinkat dispatches to NodeUnlinker and removes the child from the
// Inode tree on success.
func (c *conn) handleUnlinkat(ctx context.Context, m *p9l.Tunlinkat) proto.Message {
	fs := c.fids.get(m.DirFid)
	if fs == nil {
		return c.errorMsg(proto.EBADF)
	}

	if !validName(m.Name) {
		return c.errorMsg(proto.EINVAL)
	}

	unlinker, ok := fs.node.(NodeUnlinker)
	if !ok {
		return c.errorMsg(proto.ENOSYS)
	}

	if err := unlinker.Unlink(ctx, m.Name, m.Flags); err != nil {
		return c.errorMsg(errnoFromError(err))
	}

	// Remove child from Inode tree if parent implements InodeEmbedder.
	if parentIE, ok := fs.node.(InodeEmbedder); ok {
		parentIE.EmbeddedInode().RemoveChild(m.Name)
	}

	return &p9l.Runlinkat{}
}

// handleRenameat dispatches to NodeRenamer using AT-style directory fids.
// Both old and new directory fids are looked up independently.
func (c *conn) handleRenameat(ctx context.Context, m *p9l.Trenameat) proto.Message {
	oldDirFS := c.fids.get(m.OldDirFid)
	if oldDirFS == nil {
		return c.errorMsg(proto.EBADF)
	}

	newDirFS := c.fids.get(m.NewDirFid)
	if newDirFS == nil {
		return c.errorMsg(proto.EBADF)
	}

	if !validName(m.OldName) {
		return c.errorMsg(proto.EINVAL)
	}
	if !validName(m.NewName) {
		return c.errorMsg(proto.EINVAL)
	}

	renamer, ok := oldDirFS.node.(NodeRenamer)
	if !ok {
		return c.errorMsg(proto.ENOSYS)
	}

	if err := renamer.Rename(ctx, m.OldName, newDirFS.node, m.NewName); err != nil {
		return c.errorMsg(errnoFromError(err))
	}

	// Update Inode tree: move child from old dir to new dir.
	if oldIE, ok := oldDirFS.node.(InodeEmbedder); ok {
		oldInode := oldIE.EmbeddedInode()
		children := oldInode.Children()
		if child, found := children[m.OldName]; found {
			oldInode.RemoveChild(m.OldName)
			if newIE, ok := newDirFS.node.(InodeEmbedder); ok {
				newIE.EmbeddedInode().AddChild(m.NewName, child)
			}
		}
	}

	return &p9l.Rrenameat{}
}

// handleRename dispatches deprecated Trename (fid-based) by resolving the
// parent directory via Inode tree. Returns ENOSYS if the node does not use
// Inode embedding or the parent cannot be determined.
func (c *conn) handleRename(ctx context.Context, m *p9l.Trename) proto.Message {
	fs := c.fids.get(m.Fid)
	if fs == nil {
		return c.errorMsg(proto.EBADF)
	}

	dirFS := c.fids.get(m.DirFid)
	if dirFS == nil {
		return c.errorMsg(proto.EBADF)
	}

	if !validName(m.Name) {
		return c.errorMsg(proto.EINVAL)
	}

	// Trename is fid-based: we need the parent directory of the fid being
	// renamed. Resolve via Inode tree.
	ie, ok := fs.node.(InodeEmbedder)
	if !ok {
		return c.errorMsg(proto.ENOSYS)
	}

	parentInode := ie.EmbeddedInode().Parent()
	if parentInode == nil {
		return c.errorMsg(proto.ENOSYS)
	}

	// Find the old name by scanning parent's children for this node's Inode.
	childInode := ie.EmbeddedInode()
	var oldName string
	for name, child := range parentInode.Children() {
		if child == childInode {
			oldName = name
			break
		}
	}
	if oldName == "" {
		return c.errorMsg(proto.EINVAL)
	}

	// The parent directory's node must implement NodeRenamer.
	renamer, ok := parentInode.node.(NodeRenamer)
	if !ok {
		return c.errorMsg(proto.ENOSYS)
	}

	if err := renamer.Rename(ctx, oldName, dirFS.node, m.Name); err != nil {
		return c.errorMsg(errnoFromError(err))
	}

	// Update Inode tree: move child from parent to target dir.
	parentInode.RemoveChild(oldName)
	if targetIE, ok := dirFS.node.(InodeEmbedder); ok {
		targetIE.EmbeddedInode().AddChild(m.Name, childInode)
	}

	return &p9l.Rrename{}
}

// handleLock dispatches to NodeLocker.Lock. Requires the fid to be in opened
// state per T-04-05.
func (c *conn) handleLock(ctx context.Context, m *p9l.Tlock) proto.Message {
	fs := c.fids.get(m.Fid)
	if fs == nil {
		return c.errorMsg(proto.EBADF)
	}
	if fs.state != fidOpened {
		return c.errorMsg(proto.EBADF)
	}

	locker, ok := fs.node.(NodeLocker)
	if !ok {
		return c.errorMsg(proto.ENOSYS)
	}

	status, err := locker.Lock(ctx, m.LockType, m.Flags, m.Start, m.Length, m.ProcID, m.ClientID)
	if err != nil {
		return c.errorMsg(errnoFromError(err))
	}

	return &p9l.Rlock{Status: status}
}

// handleGetlock dispatches to NodeLocker.GetLock. Requires the fid to be in
// opened state per T-04-05.
func (c *conn) handleGetlock(ctx context.Context, m *p9l.Tgetlock) proto.Message {
	fs := c.fids.get(m.Fid)
	if fs == nil {
		return c.errorMsg(proto.EBADF)
	}
	if fs.state != fidOpened {
		return c.errorMsg(proto.EBADF)
	}

	locker, ok := fs.node.(NodeLocker)
	if !ok {
		return c.errorMsg(proto.ENOSYS)
	}

	lt, start, length, procID, clientID, err := locker.GetLock(ctx, m.LockType, m.Start, m.Length, m.ProcID, m.ClientID)
	if err != nil {
		return c.errorMsg(errnoFromError(err))
	}

	return &p9l.Rgetlock{
		LockType: lt,
		Start:    start,
		Length:   length,
		ProcID:   procID,
		ClientID: clientID,
	}
}

// handleXattrwalk handles Txattrwalk: creates a new fid in xattr read mode.
// For name != "", retrieves a single xattr. For name == "", lists all xattrs.
// RawXattrer takes precedence over simple interfaces per CONTEXT.md locked decision.
func (c *conn) handleXattrwalk(ctx context.Context, m *p9l.Txattrwalk) proto.Message {
	fs := c.fids.get(m.Fid)
	if fs == nil {
		return c.errorMsg(proto.EBADF)
	}

	// RawXattrer takes precedence over simple interfaces.
	if raw, ok := fs.node.(RawXattrer); ok {
		data, err := raw.HandleXattrwalk(ctx, m.Name)
		if err != nil {
			return c.errorMsg(errnoFromError(err))
		}
		xfs := &fidState{
			node:      fs.node,
			state:     fidXattrRead,
			xattrNode: fs.node,
			xattrName: m.Name,
			xattrData: data,
		}
		if err := c.fids.add(m.NewFid, xfs); err != nil {
			return c.errorMsg(proto.EBADF)
		}
		return &p9l.Rxattrwalk{Size: uint64(len(data))}
	}

	if m.Name == "" {
		// List mode: return null-separated list of xattr names.
		lister, ok := fs.node.(NodeXattrLister)
		if !ok {
			return c.errorMsg(proto.ENOSYS)
		}
		names, err := lister.ListXattrs(ctx)
		if err != nil {
			return c.errorMsg(errnoFromError(err))
		}
		var buf []byte
		for _, name := range names {
			buf = append(buf, []byte(name)...)
			buf = append(buf, 0)
		}
		xfs := &fidState{
			node:      fs.node,
			state:     fidXattrRead,
			xattrNode: fs.node,
			xattrData: buf,
		}
		if err := c.fids.add(m.NewFid, xfs); err != nil {
			return c.errorMsg(proto.EBADF)
		}
		return &p9l.Rxattrwalk{Size: uint64(len(buf))}
	}

	// Single xattr get.
	getter, ok := fs.node.(NodeXattrGetter)
	if !ok {
		return c.errorMsg(proto.ENOSYS)
	}
	data, err := getter.GetXattr(ctx, m.Name)
	if err != nil {
		return c.errorMsg(errnoFromError(err))
	}
	xfs := &fidState{
		node:      fs.node,
		state:     fidXattrRead,
		xattrNode: fs.node,
		xattrName: m.Name,
		xattrData: data,
	}
	if err := c.fids.add(m.NewFid, xfs); err != nil {
		return c.errorMsg(proto.EBADF)
	}
	return &p9l.Rxattrwalk{Size: uint64(len(data))}
}

// handleXattrcreate handles Txattrcreate: mutates the existing fid to xattr
// write mode. Per Pitfall 1: xattrcreate MUTATES the existing fid (does not
// create a new fid). Subsequent read/write on this fid go to the xattr buffer.
func (c *conn) handleXattrcreate(ctx context.Context, m *p9l.Txattrcreate) proto.Message {
	fs := c.fids.get(m.Fid)
	if fs == nil {
		return c.errorMsg(proto.EBADF)
	}

	// Clamp xattr buffer to msize to prevent oversized allocations (T-04-06).
	if m.AttrSize > uint64(c.msize) {
		return c.errorMsg(proto.EINVAL)
	}

	// RawXattrer takes precedence over simple interfaces.
	if raw, ok := fs.node.(RawXattrer); ok {
		writer, err := raw.HandleXattrcreate(ctx, m.Name, m.AttrSize, m.Flags)
		if err != nil {
			return c.errorMsg(errnoFromError(err))
		}
		fs.state = fidXattrWrite
		fs.xattrNode = fs.node
		fs.xattrName = m.Name
		fs.xattrSize = m.AttrSize
		fs.xattrFlags = m.Flags
		fs.xattrData = nil
		fs.xattrWriter = writer
		return &p9l.Rxattrcreate{}
	}

	// Validate the node supports xattr setting or removal.
	if m.AttrSize == 0 {
		// Size=0 is a remove operation per protocol convention.
		if _, ok := fs.node.(NodeXattrRemover); !ok {
			return c.errorMsg(proto.ENOSYS)
		}
	} else {
		if _, ok := fs.node.(NodeXattrSetter); !ok {
			return c.errorMsg(proto.ENOSYS)
		}
	}

	// Mutate the fid to xattr write mode.
	fs.state = fidXattrWrite
	fs.xattrNode = fs.node
	fs.xattrName = m.Name
	fs.xattrSize = m.AttrSize
	fs.xattrFlags = m.Flags
	fs.xattrData = nil
	fs.xattrWriter = nil

	return &p9l.Rxattrcreate{}
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

package server

import (
	"context"
	"errors"
	"log/slog"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
	"github.com/dotwaffle/ninep/proto/p9u"
)

// validatePathElement rejects 9P name components that contain embedded
// path separators or NUL bytes. Such names would traverse multiple
// directories in a single walk step or truncate at the C-layer syscall
// boundary, both of which violate 9P2000.L walk semantics. Empty names
// are also rejected. "." and ".." are permitted (spec-compliant walk
// elements).
//
// Create-style handlers (Tlcreate, Tmkdir, Tsymlink, Tlink, Tmknod,
// Trename, Trenameat) use the stricter validName helper (bridge.go),
// which additionally rejects "." and ".." -- those names are spec
// compliant for walk but not for creating or renaming entries.
func validatePathElement(name string) error {
	if name == "" {
		return proto.EINVAL
	}
	if strings.ContainsAny(name, "/\x00") {
		return proto.EINVAL
	}
	return nil
}

// dispatch routes a decoded message to the appropriate handler and returns the
// response message. It returns nil for message types that are handled inline
// (e.g. Tflush is handled in the read loop, not here). Callers are responsible
// for sending the response via sendResponse.
func (c *conn) dispatch(ctx context.Context, tag proto.Tag, msg proto.Message) proto.Message {
	switch m := msg.(type) {
	case *proto.Tattach:
		return c.handleAttach(ctx, m)
	case *proto.Twalk:
		return c.handleWalk(ctx, m)
	case *proto.Tclunk:
		return c.handleClunk(ctx, m)
	case *proto.Tremove:
		return c.handleRemove(ctx, m)
	case *proto.Tauth:
		// Auth is out of scope per project constraints.
		return c.errorMsg(proto.ENOSYS)
	case *p9l.Tlopen:
		return c.handleLopen(ctx, m)
	case *p9u.Topen:
		return c.handleUOpen(ctx, m)
	case *proto.Tread:
		return c.handleRead(ctx, m)
	case *proto.Twrite:
		return c.handleWrite(ctx, m)
	case *p9l.Tgetattr:
		return c.handleGetattr(ctx, m)
	case *p9l.Tsetattr:
		return c.handleSetattr(ctx, m)
	case *p9l.Treaddir:
		return c.handleReaddir(ctx, m)
	case *p9l.Tlcreate:
		return c.handleLcreate(ctx, m)
	case *p9u.Tcreate:
		return c.handleUCreate(ctx, m)
	case *p9l.Tmkdir:
		return c.handleMkdir(ctx, m)
	case *p9l.Tsymlink:
		return c.handleSymlink(ctx, m)
	case *p9l.Tlink:
		return c.handleLink(ctx, m)
	case *p9l.Tmknod:
		return c.handleMknod(ctx, m)
	case *p9l.Treadlink:
		return c.handleReadlink(ctx, m)
	case *p9l.Tstatfs:
		return c.handleStatfs(ctx, m)
	case *p9l.Tfsync:
		return c.handleFsync(ctx, m)
	case *p9l.Tunlinkat:
		return c.handleUnlinkat(ctx, m)
	case *p9l.Trenameat:
		return c.handleRenameat(ctx, m)
	case *p9l.Trename:
		return c.handleRename(ctx, m)
	case *p9l.Tlock:
		return c.handleLock(ctx, m)
	case *p9l.Tgetlock:
		return c.handleGetlock(ctx, m)
	case *p9l.Txattrwalk:
		return c.handleXattrwalk(ctx, m)
	case *p9l.Txattrcreate:
		return c.handleXattrcreate(ctx, m)
	case *p9u.Tstat:
		return c.handleUStat(ctx, m)
	case *p9u.Twstat:
		return c.errorMsg(proto.ENOSYS)
	default:
		return c.errorMsg(proto.ENOSYS)
	}
}

// handleAttach creates a fid pointing to the resolved root node.
func (c *conn) handleAttach(ctx context.Context, ta *proto.Tattach) proto.Message {
	// No auth support; reject Afid != NoFid.
	if ta.Afid != proto.NoFid {
		return c.errorMsg(proto.ENOSYS)
	}

	// Resolve the root node using the layered attach strategy.
	var node Node
	var err error
	switch {
	case c.server.attacher != nil:
		node, err = c.server.attacher.Attach(ctx, ta.Uname, ta.Aname)
		if err != nil {
			return c.errorMsg(errnoFromError(err))
		}
	case c.server.anames != nil && ta.Aname != "":
		var ok bool
		node, ok = c.server.anames[ta.Aname]
		if !ok {
			return c.errorMsg(proto.ENOENT)
		}
	default:
		node = c.server.root
	}

	attachPath := "/"
	if ta.Aname != "" {
		attachPath = "/" + ta.Aname
	}
	fs := &fidState{node: node, path: attachPath, state: fidAllocated}
	if err := c.fids.add(ta.Fid, fs, c.maxFids); err != nil {
		if errors.Is(err, ErrFidLimitExceeded) {
			return c.errorMsg(proto.EMFILE)
		}
		return c.errorMsg(proto.EBADF)
	}

	c.otelInst.recordFidChange(1)
	return &proto.Rattach{QID: node.QID()}
}

// handleWalk resolves a path from Fid and assigns NewFid to the result.
func (c *conn) handleWalk(ctx context.Context, tw *proto.Twalk) proto.Message {
	src := c.fids.get(tw.Fid)
	if src == nil {
		return c.errorMsg(proto.EBADF)
	}

	// nwname=0: clone fid.
	if len(tw.Names) == 0 {
		if tw.Fid == tw.NewFid {
			return &proto.Rwalk{}
		}
		// Clone: newfid points to same node with same path. Reading node and
		// path through their locked accessors matters here: another in-place
		// Twalk on src.Fid can be rewriting both concurrently.
		fs := &fidState{node: src.currentNode(), path: c.fids.getPath(tw.Fid), state: fidAllocated}
		if err := c.fids.add(tw.NewFid, fs, c.maxFids); err != nil {
			if errors.Is(err, ErrFidLimitExceeded) {
				return c.errorMsg(proto.EMFILE)
			}
			return c.errorMsg(proto.EBADF)
		}
		c.otelInst.recordFidChange(1)
		return &proto.Rwalk{}
	}

	// Validate every name component BEFORE touching any fid resolution or
	// Lookup. Names containing '/' would traverse multiple directories in a
	// single walk step (path-traversal against passthrough-backed servers
	// that pass the name straight to unix.Fstatat); NUL bytes truncate at
	// the C syscall boundary. Empty names are also invalid per spec.
	// "." and ".." remain permitted -- they are legal walk elements.
	for _, name := range tw.Names {
		if err := validatePathElement(name); err != nil {
			return c.errorMsg(proto.EINVAL)
		}
	}

	// nwname>0: walk path elements. Read through the locked accessor: a
	// second in-place Twalk on tw.Fid pipelined concurrently with this one
	// can rewrite src.node while this request is still resolving.
	current := src.currentNode()
	qids := make([]proto.QID, 0, len(tw.Names))

	for i, name := range tw.Names {
		// Check QID type first: per 9P spec, only directories can be walked.
		// With Inode embedding, all nodes satisfy NodeLookuper, so we must
		// check the QID type to distinguish files from directories.
		qid := nodeQID(current)
		if qid.Type&proto.QTDIR == 0 {
			if i == 0 {
				return c.errorMsg(proto.ENOTDIR)
			}
			break // Partial walk.
		}

		lookuper, ok := current.(NodeLookuper)
		if !ok {
			if i == 0 {
				return c.errorMsg(proto.ENOTDIR)
			}
			break // Partial walk.
		}

		child, err := lookuper.Lookup(ctx, name)
		if err != nil {
			if i == 0 {
				return c.errorMsg(errnoFromError(err))
			}
			break // Partial walk.
		}

		qids = append(qids, child.QID())
		current = child
	}

	// Complete walk: assign newfid with resolved path. Read the base path
	// through getPath: it can be rewritten concurrently by another in-place
	// Twalk on tw.Fid (see fidTable.getPath).
	if len(qids) == len(tw.Names) {
		newPath := path.Clean(c.fids.getPath(tw.Fid) + "/" + strings.Join(tw.Names, "/"))
		if tw.Fid == tw.NewFid {
			c.fids.update(tw.Fid, current)
			c.fids.setPath(tw.Fid, newPath)
		} else {
			fs := &fidState{node: current, path: newPath, state: fidAllocated}
			if err := c.fids.add(tw.NewFid, fs, c.maxFids); err != nil {
				if errors.Is(err, ErrFidLimitExceeded) {
					return c.errorMsg(proto.EMFILE)
				}
				return c.errorMsg(proto.EBADF)
			}
			c.otelInst.recordFidChange(1)
		}
	}

	return &proto.Rwalk{QIDs: qids}
}

// handleClunk removes a fid from the table. Per 9P spec, the fid is always
// invalidated, even if cleanup fails.
func (c *conn) handleClunk(ctx context.Context, tc *proto.Tclunk) proto.Message {
	fs := c.fids.clunk(tc.Fid)
	if fs == nil {
		return c.errorMsg(proto.EBADF)
	}
	c.otelInst.recordFidChange(-1)

	// Handle xattr commit/cleanup before normal clunk logic. clunk() has
	// already removed the fid from the table, so no further handler can
	// observe a state transition on it; reading under fs.mu still gives
	// the Go memory model the synchronization it needs against an
	// xattrcreate handler that may have populated these fields.
	if fs.currentState() == fidXattrWrite {
		// RawXattrer path: delegate commit to XattrWriter.
		if fs.xattrWriter != nil {
			if err := fs.xattrWriter.Commit(ctx); err != nil {
				return c.errorMsg(errnoFromError(err))
			}
			return &proto.Rclunk{}
		}

		// Simple interface path: xattrcreate flow commits the xattr on clunk.
		if fs.xattrSize == 0 {
			// Size=0 means remove xattr.
			if remover, ok := fs.xattrNode.(NodeXattrRemover); ok {
				if err := remover.RemoveXattr(ctx, fs.xattrName); err != nil {
					c.logger.Debug("xattr remove error on clunk", slog.Any("error", err))
					return c.errorMsg(errnoFromError(err))
				}
			}
			return &proto.Rclunk{}
		}
		// Validate written size matches declared size.
		if uint64(len(fs.xattrData)) != fs.xattrSize {
			return c.errorMsg(proto.EIO)
		}
		setter, ok := fs.xattrNode.(NodeXattrSetter)
		if !ok {
			return c.errorMsg(proto.ENOSYS)
		}
		if err := setter.SetXattr(ctx, fs.xattrName, fs.xattrData, fs.xattrFlags); err != nil {
			return c.errorMsg(errnoFromError(err))
		}
		return &proto.Rclunk{}
	}
	// For fidXattrRead, no commit needed -- just discard the buffer and clunk normally.

	// Release the FileHandle and close the Node now (per 9P spec, clunk
	// always succeeds), unless a beginIO-registered Read/Write/Readdir/
	// Fsync call is still in flight on this fid: releasing here would
	// close the underlying fd out from under that call, and if the OS
	// reused the fd number before the call returned, it would silently
	// operate on the wrong file. finishClunk defers the release to the
	// last such call's endIO in that case.
	fs.finishClunk(ctx, c.logger)
	return &proto.Rclunk{}
}

// handleRemove dispatches the legacy fid-based Tremove by resolving the
// parent directory via the Inode tree (mirroring handleRename, which faces
// the same fid-based-only wire shape) and calling NodeUnlinker.Unlink on
// it. Per remove(5), the fid is clunked whether or not the remove itself
// succeeds, so this always tears the fid down exactly as handleClunk does.
func (c *conn) handleRemove(ctx context.Context, tr *proto.Tremove) proto.Message {
	fs := c.fids.clunk(tr.Fid)
	if fs == nil {
		return c.errorMsg(proto.EBADF)
	}
	c.otelInst.recordFidChange(-1)

	removeErr := removeViaInodeTree(ctx, fs.currentNode())
	fs.finishClunk(ctx, c.logger)

	if removeErr != nil {
		return c.errorMsg(errnoFromError(removeErr))
	}
	return &proto.Rremove{}
}

// removeViaInodeTree resolves node's parent directory via the Inode tree
// and calls NodeUnlinker.Unlink on it with node's name in that directory.
// Legacy Tremove carries only the fid being removed, not a separate
// directory fid, so the parent must be found by walking up the tree --
// the same resolution handleRename uses for Trename.
func removeViaInodeTree(ctx context.Context, node Node) error {
	ie, ok := node.(InodeEmbedder)
	if !ok {
		return proto.ENOSYS
	}
	childInode := ie.EmbeddedInode()
	parentInode := childInode.Parent()
	if parentInode == nil {
		return proto.ENOSYS
	}

	var name string
	for n, child := range parentInode.Children() {
		if child == childInode {
			name = n
			break
		}
	}
	if name == "" {
		return proto.EINVAL
	}

	unlinker, ok := parentInode.node.(NodeUnlinker)
	if !ok {
		return proto.ENOSYS
	}
	if err := unlinker.Unlink(ctx, name, 0); err != nil {
		return err
	}
	parentInode.RemoveChild(name)
	return nil
}

// handleFlush cancels the target request's context and returns Rflush, but
// only after the flushed request's handler has written its response. 9P
// flush(5) lets a client recycle oldtag once it sees Rflush; returning Rflush
// before the flushed response is on the wire would let a spec-strict peer
// alias a late response onto a freshly issued request. Per spec Rflush is
// still returned regardless of whether the tag was found.
//
// Returns nil (and no response is sent) when the flushed handler does not
// finish within the cleanup deadline, or the connection is shutting down; in
// the deadline case the connection is closed, mirroring the Tversion drain
// fallback, rather than risk the tag-reuse aliasing.
func (c *conn) handleFlush(ctx context.Context, tf *proto.Tflush) proto.Message {
	done := c.inflight.flushWait(tf.OldTag)
	if done == nil {
		return &proto.Rflush{}
	}
	select {
	case <-done:
		return &proto.Rflush{}
	case <-ctx.Done():
		return nil
	case <-time.After(cleanupDeadline):
		c.logger.Warn("flush: timed out waiting for flushed request; closing connection",
			slog.Uint64("oldtag", uint64(tf.OldTag)),
		)
		c.signalRecvShutdown()
		_ = c.nc.Close()
		return nil
	}
}

// releaseFileHandle calls FileReleaser.Release() on a bare handle if it
// implements the interface. It is used on the lost-race paths where Open or
// Create produced a live handle but the fid transition then failed: the handle
// is reachable through no fidState, so without this it would leak (in
// passthrough, an open OS fd). Errors are logged, not propagated.
func releaseFileHandle(ctx context.Context, handle FileHandle, logger *slog.Logger) {
	if handle == nil {
		return
	}
	if rel, ok := handle.(FileReleaser); ok {
		if err := rel.Release(ctx); err != nil {
			if logger != nil {
				logger.Debug("file handle release error", slog.Any("error", err))
			}
		}
	}
}

// errnoFromError converts a Go error to a proto.Errno. If the error wraps or
// is a proto.Errno, that value is returned directly. If the error wraps a
// syscall.Errno (e.g. from os.PathError, unix.Stat, or passthrough syscalls),
// it is translated through proto.ErrnoFromUnix -- on Linux this is identity;
// on FreeBSD it maps the FreeBSD errno number to the corresponding Linux wire
// value (9P2000.L defines errno numbers as Linux UAPI values).
//
// This lets node implementations return raw syscall.Errno without wrapping;
// the dispatch layer ensures the wire format remains Linux-numbered regardless
// of the host OS.
//
// Unknown errors (including nil) default to proto.EIO.
func errnoFromError(err error) proto.Errno {
	if errno, ok := errors.AsType[proto.Errno](err); ok {
		return errno
	}
	if errno, ok := errors.AsType[syscall.Errno](err); ok {
		return proto.ErrnoFromUnix(errno)
	}
	return proto.EIO
}

// Compile-time assertion that error message types are kept in sync.
var (
	_ proto.Message = (*p9l.Rlerror)(nil)
	_ proto.Message = (*p9u.Rerror)(nil)
)

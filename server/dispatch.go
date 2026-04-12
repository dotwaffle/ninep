package server

import (
	"context"
	"errors"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
	"github.com/dotwaffle/ninep/proto/p9u"
)

// dispatch routes a decoded message to the appropriate handler.
func (c *conn) dispatch(ctx context.Context, tag proto.Tag, msg proto.Message) {
	var resp proto.Message
	switch m := msg.(type) {
	case *proto.Tattach:
		resp = c.handleAttach(ctx, m)
	case *proto.Twalk:
		resp = c.handleWalk(ctx, m)
	case *proto.Tclunk:
		resp = c.handleClunk(ctx, m)
	case *proto.Tflush:
		resp = c.handleFlush(ctx, m)
	case *proto.Tauth:
		// Auth is out of scope per project constraints.
		c.sendError(tag, proto.ENOSYS)
		return
	default:
		c.sendError(tag, proto.ENOSYS)
		return
	}
	c.sendResponse(tag, resp)
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

	fs := &fidState{node: node, state: fidAllocated}
	if err := c.fids.add(ta.Fid, fs); err != nil {
		return c.errorMsg(proto.EBADF)
	}

	return &proto.Rattach{QID: node.QID()}
}

// handleWalk resolves a path from Fid and assigns NewFid to the result.
func (c *conn) handleWalk(_ context.Context, tw *proto.Twalk) proto.Message {
	src := c.fids.get(tw.Fid)
	if src == nil {
		return c.errorMsg(proto.EBADF)
	}

	// nwname=0: clone fid.
	if len(tw.Names) == 0 {
		if tw.Fid == tw.NewFid {
			return &proto.Rwalk{}
		}
		// Clone: newfid points to same node.
		fs := &fidState{node: src.node, state: fidAllocated}
		if err := c.fids.add(tw.NewFid, fs); err != nil {
			return c.errorMsg(proto.EBADF)
		}
		return &proto.Rwalk{}
	}

	// nwname>0: walk path elements.
	current := src.node
	qids := make([]proto.QID, 0, len(tw.Names))

	for i, name := range tw.Names {
		lookuper, ok := current.(NodeLookuper)
		if !ok {
			if i == 0 {
				return c.errorMsg(proto.ENOTDIR)
			}
			break // Partial walk.
		}

		child, err := lookuper.Lookup(context.Background(), name)
		if err != nil {
			if i == 0 {
				return c.errorMsg(errnoFromError(err))
			}
			break // Partial walk.
		}

		qids = append(qids, child.QID())
		current = child
	}

	// Complete walk: assign newfid.
	if len(qids) == len(tw.Names) {
		if tw.Fid == tw.NewFid {
			c.fids.update(tw.Fid, current)
		} else {
			fs := &fidState{node: current, state: fidAllocated}
			if err := c.fids.add(tw.NewFid, fs); err != nil {
				return c.errorMsg(proto.EBADF)
			}
		}
	}

	return &proto.Rwalk{QIDs: qids}
}

// handleClunk removes a fid from the table. Per 9P spec, the fid is always
// invalidated, even if cleanup fails.
func (c *conn) handleClunk(_ context.Context, tc *proto.Tclunk) proto.Message {
	fs := c.fids.clunk(tc.Fid)
	if fs == nil {
		return c.errorMsg(proto.EBADF)
	}
	// Phase 3 will add FileHandle.Close() call here.
	return &proto.Rclunk{}
}

// handleFlush is a stub for Plan 03. Always responds Rflush per spec.
func (c *conn) handleFlush(_ context.Context, _ *proto.Tflush) proto.Message {
	return &proto.Rflush{}
}

// errnoFromError converts a Go error to a proto.Errno. If the error wraps or
// is a proto.Errno, that value is returned. Otherwise EIO is used as the
// default.
func errnoFromError(err error) proto.Errno {
	var errno proto.Errno
	if errors.As(err, &errno) {
		return errno
	}
	return proto.EIO
}

// Compile-time assertion that error message types are kept in sync.
var (
	_ proto.Message = (*p9l.Rlerror)(nil)
	_ proto.Message = (*p9u.Rerror)(nil)
)

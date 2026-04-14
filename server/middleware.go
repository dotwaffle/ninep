package server

import (
	"context"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
	"github.com/dotwaffle/ninep/proto/p9u"
)

// Handler processes a decoded 9P message and returns the response. Middleware
// wraps Handler values to add cross-cutting behavior (tracing, metrics, logging)
// without modifying dispatch logic.
type Handler func(ctx context.Context, tag proto.Tag, msg proto.Message) proto.Message

// Middleware wraps a Handler, adding behavior before and/or after dispatch.
// Compose by stacking: the first middleware added is outermost (first to
// execute, last to see the response).
type Middleware func(next Handler) Handler

// WithMiddleware adds middleware to the server's dispatch chain. Middleware runs
// in order: the first added is outermost (first to execute). Multiple calls
// append to the existing chain.
func WithMiddleware(mw ...Middleware) Option {
	return func(s *Server) {
		s.middlewares = append(s.middlewares, mw...)
	}
}

// chain builds a Handler by wrapping inner with the given middleware. The first
// middleware in the slice is outermost (first to execute). If mws is nil or
// empty, inner is returned directly with zero overhead.
func chain(inner Handler, mws []Middleware) Handler {
	if len(mws) == 0 {
		return inner
	}
	h := inner
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// isErrorResponse returns true when msg is a protocol error response (Rlerror
// or Rerror). Used by observability middleware to detect error outcomes.
func isErrorResponse(msg proto.Message) bool {
	t := msg.Type()
	return t == proto.TypeRlerror || t == proto.TypeRerror
}

// fidFromMessage extracts the primary Fid from a T-message. For messages that
// do not carry a Fid (responses, Tflush, Tversion, etc.), it returns 0, false.
func fidFromMessage(msg proto.Message) (proto.Fid, bool) {
	switch m := msg.(type) {
	// Shared base T-messages.
	case *proto.Tattach:
		return m.Fid, true
	case *proto.Twalk:
		return m.Fid, true
	case *proto.Tclunk:
		return m.Fid, true
	case *proto.Tread:
		return m.Fid, true
	case *proto.Twrite:
		return m.Fid, true
	case *proto.Tremove:
		return m.Fid, true

	// 9P2000.L T-messages.
	case *p9l.Tlopen:
		return m.Fid, true
	case *p9l.Tgetattr:
		return m.Fid, true
	case *p9l.Tsetattr:
		return m.Fid, true
	case *p9l.Treaddir:
		return m.Fid, true
	case *p9l.Tlcreate:
		return m.Fid, true
	case *p9l.Tmkdir:
		return m.DirFid, true
	case *p9l.Tsymlink:
		return m.DirFid, true
	case *p9l.Tlink:
		return m.DirFid, true
	case *p9l.Tmknod:
		return m.DirFid, true
	case *p9l.Treadlink:
		return m.Fid, true
	case *p9l.Tstatfs:
		return m.Fid, true
	case *p9l.Tfsync:
		return m.Fid, true
	case *p9l.Tunlinkat:
		return m.DirFid, true
	case *p9l.Trenameat:
		return m.OldDirFid, true
	case *p9l.Trename:
		return m.Fid, true
	case *p9l.Tlock:
		return m.Fid, true
	case *p9l.Tgetlock:
		return m.Fid, true
	case *p9l.Txattrwalk:
		return m.Fid, true
	case *p9l.Txattrcreate:
		return m.Fid, true

	// 9P2000.u T-messages (handled via dispatch in p9u mode).
	case *p9u.Topen:
		return m.Fid, true
	case *p9u.Tcreate:
		return m.Fid, true
	case *p9u.Tstat:
		return m.Fid, true
	case *p9u.Twstat:
		return m.Fid, true

	default:
		return 0, false
	}
}

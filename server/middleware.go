package server

import (
	"context"
	"errors"
	"slices"

	"github.com/dotwaffle/ninep/proto"
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
		for _, middleware := range mw {
			if middleware == nil {
				s.setConfigError(errors.New("server: middleware must not be nil"))
				return
			}
		}
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
	for _, v := range slices.Backward(mws) {
		h = v(h)
	}
	return h
}

// isErrorResponse returns true when msg is a protocol error response (Rlerror
// or Rerror). Used by observability middleware to detect error outcomes.
func isErrorResponse(msg proto.Message) bool {
	t := msg.Type()
	return t == proto.TypeRlerror || t == proto.TypeRerror
}

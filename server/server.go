package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

// Server serves the 9P protocol over network connections. Create with New.
type Server struct {
	root        Node
	maxMsize    uint32
	maxInflight int
	idleTimeout time.Duration // 0 = no timeout (GO-SEC-1)
	logger      *slog.Logger
	anames      map[string]Node
	attacher    Attacher
}

// New creates a Server rooted at the given Node. Options configure behavior.
// The root must implement NodeLookuper for walk resolution.
func New(root Node, opts ...Option) *Server {
	s := &Server{
		root:        root,
		maxMsize:    131072, // 128KB default
		maxInflight: 64,
		logger:      slog.Default(),
		// idleTimeout: 0 (zero value = no timeout)
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Serve accepts connections from ln and serves each one in a new goroutine.
// It blocks until the context is cancelled or the listener returns an error.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		nc, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("accept: %w", err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.ServeConn(ctx, nc)
		}()
	}
}

// ServeConn serves a single 9P connection. It blocks until the connection is
// closed or the context is cancelled.
func (s *Server) ServeConn(ctx context.Context, nc net.Conn) {
	c := newConn(s, nc)
	c.serve(ctx)
}

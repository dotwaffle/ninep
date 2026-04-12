package server

import (
	"log/slog"
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

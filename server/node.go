package server

import (
	"context"

	"github.com/dotwaffle/ninep/proto"
)

// Node is the minimal interface every filesystem node must implement.
// Phase 3 expands with capability interfaces (NodeReader, NodeWriter, etc.)
// that are type-asserted at dispatch time.
type Node interface {
	// QID returns the server's unique identifier for this node.
	QID() proto.QID
}

// NodeLookuper is implemented by directory nodes that can resolve child names.
// Walk calls Lookup for each path element.
type NodeLookuper interface {
	// Lookup resolves a child by name, returning the child Node or an error.
	// Return proto.ENOENT (wrapped) if the name does not exist.
	Lookup(ctx context.Context, name string) (Node, error)
}

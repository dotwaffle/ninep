package server

import "context"

type connKey struct{}

// ConnInfo exposes per-connection metadata to node handlers. A pointer to
// ConnInfo is injected into the request context by the server before each
// handler invocation; retrieve it with ConnFromContext.
//
// Fields are read-only. Callers MUST NOT mutate a ConnInfo returned from
// ConnFromContext -- the same pointer is shared across every request on
// the same connection, and mutation would race with concurrent request
// goroutines and corrupt observability labels.
type ConnInfo struct {
	Protocol   string // "9P2000.L" or "9P2000.u"
	Msize      uint32 // Negotiated message size
	RemoteAddr string // Remote address of the client
}

// ConnFromContext returns the connection info for the current request.
// Returns nil if not called within a connection handler.
//
// There is intentionally no NewContext(ctx, *ConnInfo) helper: ConnInfo
// is injected by the server from negotiated connection state, and user
// code cannot construct a valid ConnInfo externally. This asymmetric
// accessor pattern mirrors stdlib context keys that represent
// server-owned state (e.g., the net/http pattern for server-injected values).
func ConnFromContext(ctx context.Context) *ConnInfo {
	ci, _ := ctx.Value(connKey{}).(*ConnInfo)
	return ci
}

func withConnInfo(ctx context.Context, ci *ConnInfo) context.Context {
	return context.WithValue(ctx, connKey{}, ci)
}

package server

import "context"

type connKey struct{}

// ConnInfo exposes per-connection metadata to node handlers.
type ConnInfo struct {
	Protocol   string // "9P2000.L" or "9P2000.u"
	Msize      uint32 // Negotiated message size
	RemoteAddr string // Remote address of the client
}

// ConnFromContext returns the connection info for the current request.
// Returns nil if not called within a connection handler.
func ConnFromContext(ctx context.Context) *ConnInfo {
	ci, _ := ctx.Value(connKey{}).(*ConnInfo)
	return ci
}

func withConnInfo(ctx context.Context, ci *ConnInfo) context.Context {
	return context.WithValue(ctx, connKey{}, ci)
}

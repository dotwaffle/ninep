package client

// Msize returns the negotiated maximum message size, in bytes. Set at
// Dial time as the minimum of the client's proposed msize (see
// [WithMsize]) and the server's Rversion.Msize cap. Immutable for the
// Conn's lifetime -- 9P does not support mid-connection renegotiation
// from the client side in this library.
//
// Used by [File.Read] / [File.Write] (Plan 20-03+) to clamp each
// Tread/Twrite payload so the encoded frame fits within the negotiated
// msize after per-message framing overhead.
func (c *Conn) Msize() uint32 {
	return c.msize
}

// Dialect returns the negotiated 9P dialect: "9P2000.L" or "9P2000.u".
// Callers that branch on dialect for advanced ops should compare
// against these exact strings.
//
// The dialect is chosen by Dial's Tversion round-trip per Phase 19 D-09
// (see client/doc.go "Dialects"). A bare "9P2000" response is
// normalized to "9P2000.u" at Dial time (Linux v9fs kernel convention);
// Dialect never returns "9P2000". Immutable for the Conn's lifetime.
func (c *Conn) Dialect() string {
	return c.dialect.String()
}

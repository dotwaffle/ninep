package client

import (
	"io"
	"log/slog"
	"net"
	"sync"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
	"github.com/dotwaffle/ninep/proto/p9u"
)

// protocol identifies the negotiated 9P dialect for a Conn. Stored on Conn
// after Dial's Tversion round-trip completes; immutable for the Conn's
// lifetime (9P does not support mid-connection re-negotiation from the
// client side in this library).
type protocol uint8

const (
	protocolNone protocol = iota
	protocolL             // 9P2000.L
	protocolU             // 9P2000.u (also selected when the server replies bare "9P2000" per D-09; Linux v9fs kernel convention.)
)

// String returns the version string for the protocol dialect.
func (p protocol) String() string {
	switch p {
	case protocolL:
		return "9P2000.L"
	case protocolU:
		return "9P2000.u"
	default:
		return "unknown"
	}
}

// codec mirrors server/conn.go's function-pointer codec struct. The
// encode/decode function pointers are set once at Tversion time and are
// the only per-op dispatch indirection on the hot path. An interface
// method-set would add vtable indirection; the function-pointer form
// matches the server's proven pattern.
type codec struct {
	encode func(w io.Writer, tag proto.Tag, msg proto.Message) error
	decode func(r io.Reader) (proto.Tag, proto.Message, error)
}

var (
	codecL = codec{encode: p9l.Encode, decode: p9l.Decode}
	codecU = codec{encode: p9u.Encode, decode: p9u.Decode}
)

// Conn is a 9P client connection. Safe for concurrent use by multiple
// goroutines per D-07; modeled on database/sql.DB. All T-message writes
// are serialized through writeMu; all R-message reads come from a single
// read goroutine that dispatches into per-tag response channels stored in
// inflight.
//
// Lifetime invariants:
//
//   - Construction is via Dial. Fields below are set once during Dial and
//     then read-only for the Conn's lifetime (except inflight's internal
//     mutex, writeMu, and the sync.WaitGroup counters).
//   - closeCh is closed exactly once via closeOnce. After close, the read
//     goroutine exits, writeT returns an error, and tagAllocator.acquire
//     returns ErrClosed.
//   - readerWG tracks the single read goroutine. callerWG tracks every
//     op-method goroutine that has an acquired tag. Both are drained by
//     Close/Shutdown (Plan 19-05).
type Conn struct {
	nc      net.Conn
	dialect protocol
	msize   uint32
	codec   codec

	tags     *tagAllocator
	inflight *inflightMap

	// writeMu serializes all net.Conn.Write calls (T-message sends +
	// ctor-time Tversion send). Mirrors server/conn.go's writeMu.
	writeMu sync.Mutex
	// encHdr holds the 7-byte T-message header between fill and writev
	// inside writeT. Guarded by writeMu; conn-resident so no per-send heap
	// escape.
	encHdr [proto.HeaderSize]byte
	// encBufsArr is the backing array for the net.Buffers slice used in
	// writeT. Two entries suffice: hdr + body. Re-sliced on every call
	// because net.Buffers.WriteTo mutates both len AND cap of its
	// receiver on full consumption (see internal/wire.WriteFramesLocked
	// godoc + ninep CLAUDE.md §Performance). Guarded by writeMu.
	encBufsArr [2][]byte

	// closeCh is closed exactly once by closeOnce to signal shutdown to the
	// read goroutine and any caller blocked in tagAllocator.acquire.
	closeCh   chan struct{}
	closeOnce sync.Once

	// callerWG tracks outstanding op-method goroutines (each holds a tag).
	// readerWG tracks the single read goroutine. Both are awaited by
	// Close/Shutdown in Plan 19-05.
	callerWG sync.WaitGroup
	readerWG sync.WaitGroup

	logger *slog.Logger
}

// Close is a placeholder for Plan 19-05's drain sequence. Task 3 wires it
// to signalShutdown via the real readLoop so TestDial_SpawnsReadGoroutine
// and the pair helper cleanup path can tear down the read goroutine cleanly
// without the full drain-with-timeout logic that Plan 19-05 ships.
//
// TODO(plan-19-05): replace with drain/shutdown sequence honoring
// WithCloseTimeout and returning any drain error.
func (c *Conn) Close() error {
	c.signalShutdown()
	c.readerWG.Wait()
	return nil
}

// isClosed returns true once signalShutdown has fired. Non-blocking
// (closeCh is the single source of truth; see signalShutdown in
// read_loop.go). Used by writeT's pre-flight check and by Plan 19-04's
// op methods to short-circuit a closed-Conn return before paying the
// tagAllocator.acquire round-trip.
func (c *Conn) isClosed() bool {
	select {
	case <-c.closeCh:
		return true
	default:
		return false
	}
}

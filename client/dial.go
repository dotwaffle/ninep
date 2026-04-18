package client

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"time"

	"github.com/dotwaffle/ninep/internal/wire"
	"github.com/dotwaffle/ninep/proto"
)

// minMsize mirrors server/conn.go's minMsize. A negotiated msize below this
// cannot carry a useful Rread/Rwrite payload: the 7-byte header plus small
// encoded fields (e.g. Rlerror.Ecode[4] or Rattach.QID[13]) leaves almost
// nothing for data.
const minMsize = 256

// Dial returns a live Conn after running 9P version negotiation over nc.
// The client proposes 9P2000.L with WithMsize's value (default 1 MiB; see
// client/options.go).
//
// Per D-09 (.planning/phases/19/19-CONTEXT.md), the server's Rversion is
// accepted when it carries one of three strings:
//
//   - "9P2000.L" — full .L codec + message set.
//   - "9P2000.u" — .u codec + .u message set (Unix extensions explicit).
//   - "9P2000"   — bare. Linux v9fs treats this as a .u-compatible alias
//     (the kernel client proposes .u and the server may echo the bare
//     string). Dial maps this to the .u codec to match that convention.
//
// Any other version string yields ErrVersionMismatch. The negotiated msize
// is min(client proposal, server Rversion.Msize); a result below minMsize
// (256) yields ErrMsizeTooSmall.
//
// The supplied ctx is honored only during the Tversion round-trip — its
// deadline is applied to nc via SetDeadline and cleared before Dial returns
// on success. For request-level cancellation (Phase 22) use per-op contexts
// on the returned *Conn.
//
// On any error Dial leaves nc in whatever state the caller provided: it
// does NOT close nc on its own, so a caller may reuse the connection
// (e.g. re-dial with a different proposed version).
func Dial(ctx context.Context, nc net.Conn, opts ...Option) (*Conn, error) {
	cfg := newConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	// Honour a pre-cancelled ctx before touching the wire.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("client.Dial: %w", err)
	}

	// Apply ctx deadline to the one-shot negotiation. Cleared on success.
	// A ctx without deadline leaves nc with no deadline; caller-provided
	// deadlines propagate through.
	if deadline, ok := ctx.Deadline(); ok {
		if err := nc.SetDeadline(deadline); err != nil {
			return nil, fmt.Errorf("client.Dial: set negotiation deadline: %w", err)
		}
	}

	// 1. Encode Tversion body into a cold-path buffer. No bufpool — this
	//    runs once per Conn and keeping allocation explicit simplifies the
	//    error-unwind paths that follow.
	tver := &proto.Tversion{Msize: cfg.msize, Version: "9P2000.L"}
	body := new(bytes.Buffer)
	if err := tver.EncodeTo(body); err != nil {
		return nil, fmt.Errorf("client.Dial: encode Tversion: %w", err)
	}

	// 2. Write framed Tversion: size[4] + type[1] + tag[2] + body.
	size := uint32(proto.HeaderSize) + uint32(body.Len())
	var hdr [proto.HeaderSize]byte
	binary.LittleEndian.PutUint32(hdr[0:4], size)
	hdr[4] = uint8(proto.TypeTversion)
	binary.LittleEndian.PutUint16(hdr[5:7], uint16(proto.NoTag))
	if _, err := nc.Write(hdr[:]); err != nil {
		return nil, fmt.Errorf("client.Dial: write Tversion header: %w", err)
	}
	if _, err := nc.Write(body.Bytes()); err != nil {
		return nil, fmt.Errorf("client.Dial: write Tversion body: %w", err)
	}

	// 3. Read Rversion via the wire.ReadSize + wire.ReadBody split.
	//    msize validation sits between the two reads so a malicious 4 GiB
	//    size field cannot coerce a body allocation before policy is
	//    consulted (research §Open Question 2 + Pitfall 10-B).
	rsize, err := wire.ReadSize(nc)
	if err != nil {
		return nil, fmt.Errorf("client.Dial: read Rversion size: %w", err)
	}
	if rsize > cfg.msize {
		return nil, fmt.Errorf("client.Dial: invalid Rversion size %d (proposed msize %d)", rsize, cfg.msize)
	}
	// bodyLen = size - 4; we already consumed the 4-byte prefix in ReadSize.
	bodyLen := int(rsize) - 4
	bodyBuf := make([]byte, bodyLen)
	if err := wire.ReadBody(nc, bodyBuf); err != nil {
		return nil, fmt.Errorf("client.Dial: read Rversion body: %w", err)
	}

	// 4. Parse Rversion: type[1] + tag[2] + Msize[4] + Version[s].
	if len(bodyBuf) < 3 {
		return nil, fmt.Errorf("client.Dial: Rversion body too small: %d bytes", len(bodyBuf))
	}
	rtype := proto.MessageType(bodyBuf[0])
	rtag := proto.Tag(binary.LittleEndian.Uint16(bodyBuf[1:3]))
	if rtype != proto.TypeRversion {
		return nil, fmt.Errorf("client.Dial: expected Rversion, got %v", rtype)
	}
	if rtag != proto.NoTag {
		return nil, fmt.Errorf("client.Dial: Rversion tag %d != NoTag", rtag)
	}
	var rver proto.Rversion
	if err := rver.DecodeFrom(bytes.NewReader(bodyBuf[3:])); err != nil {
		return nil, fmt.Errorf("client.Dial: decode Rversion: %w", err)
	}

	// 5. Select codec + dialect per D-09. Bare "9P2000" maps to .u (Linux
	//    v9fs kernel convention); "9P2000.u" maps to .u; "9P2000.L" maps to
	//    .L; anything else is ErrVersionMismatch.
	var dialect protocol
	var cc codec
	switch rver.Version {
	case "9P2000.L":
		dialect = protocolL
		cc = codecL
	case "9P2000.u", "9P2000":
		// "9P2000.u" — explicit Unix-extensions advertisement.
		// "9P2000"   — bare 9P2000; Linux v9fs treats this as .u-alias.
		// Both paths use the p9u codec and the .u R-message factory.
		dialect = protocolU
		cc = codecU
	default:
		return nil, fmt.Errorf("%w: server returned %q", ErrVersionMismatch, rver.Version)
	}

	// 6. msize = min(client proposal, server Rversion.Msize). Floor at 256.
	negotiated := cfg.msize
	if rver.Msize < negotiated {
		negotiated = rver.Msize
	}
	if negotiated < minMsize {
		return nil, fmt.Errorf("%w: negotiated %d < %d", ErrMsizeTooSmall, negotiated, minMsize)
	}

	// 7. Clear the negotiation deadline. A live Conn has no implicit
	//    deadline; per-op contexts drive per-request cancellation
	//    (Phase 22). Passing the zero time.Time disables the deadline.
	if err := nc.SetDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("client.Dial: clear deadline: %w", err)
	}

	// 8. Construct the Conn + spawn the read goroutine.
	c := &Conn{
		nc:       nc,
		dialect:  dialect,
		msize:    negotiated,
		codec:    cc,
		tags:     newTagAllocator(cfg.maxInflight),
		inflight: newInflightMap(),
		fids:     newFidAllocator(),
		closeCh:  make(chan struct{}),
		logger:   cfg.logger,
	}
	c.readerWG.Add(1)
	go c.readLoop()
	return c, nil
}

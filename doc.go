// Package ninep implements the 9P2000.L and 9P2000.u network filesystem
// protocols in Go: a capability-based server, a wire-level client, and
// the shared protocol types they build on.
//
// The server API follows go-fuse/v2/fs: a filesystem node embeds
// [server.Inode] and implements only the capability interfaces it
// supports (e.g. [server.NodeReader], [server.NodeWriter],
// [server.NodeOpener]); every unimplemented operation returns ENOSYS
// through the Inode defaults, so there is no boilerplate for
// operations a filesystem does not have.
//
// # Design Notes
//
//   - Dependencies are kept to the standard library plus the
//     OpenTelemetry API and golang.org/x/sys.
//   - Wire encoding uses direct [encoding/binary.LittleEndian] byte
//     manipulation, pooled buffers, and writev for payloads; the
//     reflection-based binary.Read/Write API is not used. Hot paths
//     avoid allocation where practical.
//   - Observability is OpenTelemetry: optional per-operation traces and
//     metrics, plus [log/slog] logging with trace-ID correlation in the
//     server dispatch path. All of it is disabled (and costs nothing)
//     unless configured.
//
// # Package Layout
//
//   - [github.com/dotwaffle/ninep/server] - the capability-based 9P
//     server: connection handling, options, dispatch middleware.
//   - [github.com/dotwaffle/ninep/client] - a concurrent wire-level 9P
//     client that multiplexes requests over any [net.Conn], with an
//     io.Reader/Writer-compliant File handle surface.
//   - [github.com/dotwaffle/ninep/client/clienttest] - server+client
//     test pair helpers, in the spirit of net/http/httptest.
//   - [github.com/dotwaffle/ninep/proto] - shared wire types, frame
//     encoding/decoding, and errno mappings.
//   - [github.com/dotwaffle/ninep/server/memfs] - in-memory filesystem
//     nodes with a builder API, for tests and virtual filesystems.
//   - [github.com/dotwaffle/ninep/server/passthrough] - a Linux/FreeBSD
//     passthrough filesystem exposing a host directory via *at
//     syscalls.
//   - [github.com/dotwaffle/ninep/server/fstest] - a protocol-level
//     test harness for validating filesystem implementations.
//   - [github.com/dotwaffle/ninep/vsock] - AF_VSOCK Listen/Dial for 9P
//     over virtio-vsock guest/host connections.
//
// # Example: Hello World File Server
//
// A complete, minimal 9P2000.L server serving a static in-memory file:
//
//	package main
//
//	import (
//		"context"
//		"log"
//		"net"
//
//		"github.com/dotwaffle/ninep/proto"
//		"github.com/dotwaffle/ninep/server"
//	)
//
//	// HelloFile serves a static "hello world" file.
//	type HelloFile struct {
//		server.Inode
//	}
//
//	// Getattr implements server.NodeGetattrer.
//	func (f *HelloFile) Getattr(_ context.Context, _ proto.AttrMask) (proto.Attr, error) {
//		return proto.Attr{
//			Valid: proto.AttrMode | proto.AttrSize,
//			Mode:  0o444, // read-only file
//			Size:  11,
//		}, nil
//	}
//
//	// Read implements server.NodeReader.
//	func (f *HelloFile) Read(_ context.Context, buf []byte, offset uint64) (int, error) {
//		data := []byte("hello world")
//		if offset >= uint64(len(data)) {
//			return 0, nil
//		}
//		end := min(offset+uint64(len(buf)), uint64(len(data)))
//		return copy(buf, data[offset:end]), nil
//	}
//
//	func main() {
//		root := &HelloFile{}
//		root.Init(proto.QID{Type: proto.QTFILE, Path: 1}, root)
//
//		srv := server.New(root)
//		ln, err := net.Listen("tcp", ":5640")
//		if err != nil {
//			log.Fatal(err)
//		}
//		log.Fatal(srv.Serve(context.Background(), ln))
//	}
//
// # Protocol Specifications
//
//   - Linux kernel 9P driver documentation: https://docs.kernel.org/filesystems/9p.html
//   - Plan 9 manual pages (Section 5): https://man.cat-v.org/plan_9
//   - 9P2000.L protocol extension: https://wiki.qemu.org/Documentation/9p2000.L
package ninep

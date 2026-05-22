// Package ninep is a high-performance, modern Go implementation of the 9P2000.L
// (Linux-native) and 9P2000.u (legacy UNIX) network filesystem protocols. It provides
// a capability-based API inspired by go-fuse/v2/fs, allowing filesystem developers
// to embed a base Inode and implement only the interfaces they need.
//
// # Architecture & Core Tenets
//
//   - Capability-Based API: File server implementers only embed [server.Inode] and
//     implement the specific capability interfaces (e.g., [server.NodeReader],
//     [server.NodeWriter], [server.NodeOpener]) that their filesystem supports.
//     All unsupported operations automatically return ENOSYS (not supported) errors,
//     eliminating monolithic boilerplate.
//   - Stdlib-First: Keep dependencies strictly minimal, adhering to standard Go
//     idioms, while remaining lightweight and portable.
//   - Modern Idioms: Built for Go 1.26+, utilizing standard features like generics,
//     any instead of interface{}, modern structured logging ([log/slog]), and robust
//     multi-error handling ([errors.Join], [errors.Is]).
//   - High Performance & Zero-Allocation Hot Paths: Avoids Go reflection (`binary.Read`/`Write`)
//     entirely on critical paths. It uses zero-copy buffer pools, `writev` for payloads,
//     and direct byte-level manipulation using binary.LittleEndian.
//   - Observability: Integrates deeply with OpenTelemetry ([go.opentelemetry.io/otel]) for
//     traces and metrics, and utilizes slog structured logging with automated trace ID
//     correlation in server dispatching.
//
// # Package Layout
//
// The module is split into several logical packages:
//
//   - [github.com/dotwaffle/ninep/server] - Contains the core capability-based 9P server
//     implementation, connection manager, options, and dispatch middleware.
//   - [github.com/dotwaffle/ninep/client] - A high-performance, concurrent, wire-level 9P
//     client that multiplexes requests over any [net.Conn] with a standard, io.Reader/Writer-compliant
//     File handle surface.
//   - [github.com/dotwaffle/ninep/proto] - Shared wire-level types, constants, encoding/decoding
//     frames, and portable errno mappings.
//   - [github.com/dotwaffle/ninep/server/memfs] - A fully in-memory, thread-safe filesystem helper
//     featuring a fluent builder API, designed for mock testing and virtual file systems.
//   - [github.com/dotwaffle/ninep/server/passthrough] - A high-performance Linux-only passthrough
//     filesystem that safely exposes host directories via modern *at syscalls.
//   - [github.com/dotwaffle/ninep/server/fstest] - A protocol-level test harness to comprehensively
//     validate custom 9P filesystem implementations.
//
// # Example: Hello World File Server
//
// Below is a complete, minimal 9P2000.L server serving a static in-memory file:
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
//	// Getattr implements the server.NodeGetattrer interface to define file metadata.
//	func (f *HelloFile) Getattr(_ context.Context, _ proto.AttrMask) (proto.Attr, error) {
//		return proto.Attr{
//			Valid: proto.AttrMode | proto.AttrSize,
//			Mode:  0o444, // read-only file
//			Size:  11,
//		}, nil
//	}
//
//	// Read implements the server.NodeReader interface to serve file data.
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
//		// Initialize the root node with QID metadata
//		root := &HelloFile{}
//		root.Init(proto.QID{Type: proto.QTFILE, Path: 1}, root)
//
//		// Instatiate and serve the 9P server
//		srv := server.New(root)
//		ln, err := net.Listen("tcp", ":5640")
//		if err != nil {
//			log.Fatal(err)
//		}
//		log.Fatal(srv.Serve(context.Background(), ln))
//	}
//
// # Protocol Specifications & Resources
//
//   - Linux kernel 9P driver documentation: https://docs.kernel.org/filesystems/9p.html
//   - Plan 9 manual pages (Section 5): https://man.cat-v.org/plan_9
//   - The original 9P2000.L protocol extension details: https://wiki.qemu.org/Documentation/9p2000.L
package ninep

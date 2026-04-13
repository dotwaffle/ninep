// Package server implements a 9P2000.L file server with a capability-based
// API inspired by go-fuse/v2/fs. Filesystem authors embed [*Inode] in their
// node types and implement only the capability interfaces they need; all
// unimplemented operations automatically return ENOSYS.
//
// # Capability Pattern
//
// Define a struct, embed [*Inode], and implement capability interfaces such as
// [NodeReader], [NodeWriter], [NodeOpener], [NodeGetattrer], [NodeReaddirer],
// and others. The server's dispatch layer detects implemented interfaces at
// runtime and routes 9P messages accordingly.
//
//	type MyFile struct {
//	    server.Inode
//	}
//
//	func (f *MyFile) Read(ctx context.Context, offset uint64, count uint32) ([]byte, error) {
//	    return []byte("hello"), nil
//	}
//
// Approximately 22 capability interfaces are defined (see node.go), covering
// file I/O, directory operations, symlinks, device nodes, xattrs, locking,
// and filesystem statistics.
//
// # Composable Helpers
//
// [ReadOnlyFile] and [ReadOnlyDir] are pre-built composable types that embed
// Inode and signal intent: a read-only file that cannot be written, a
// read-only directory that cannot be mutated.
//
// # FileHandle
//
// [NodeOpener] returns a [FileHandle] to carry per-open state. If the
// FileHandle implements [FileReader], [FileWriter], or [FileReaddirer], those
// take priority over the corresponding Node-level methods for that open
// instance. This allows stateful I/O (e.g., seekable directory enumeration)
// without polluting the node itself.
//
// # Server Lifecycle
//
// Create a server with [New], passing the root [Node] and any [Option] values.
// Call [Server.Serve] with a context and [net.Listener] to accept connections:
//
//	srv := server.New(root,
//	    server.WithMaxMsize(1 << 20),
//	    server.WithLogger(slog.Default()),
//	)
//	ln, _ := net.Listen("tcp", ":5640")
//	srv.Serve(ctx, ln)
//
// Each accepted connection runs in its own goroutine. Within a connection, a
// single writer goroutine serializes responses while requests are dispatched
// concurrently (bounded by [WithMaxInflight]).
//
// # Functional Options
//
// Configure the server with:
//   - [WithMaxMsize] -- maximum negotiated message size (default 128KB)
//   - [WithMaxInflight] -- concurrent request limit per connection (default 64)
//   - [WithLogger] -- structured logger with automatic trace ID correlation
//   - [WithIdleTimeout] -- per-connection idle timeout
//   - [WithAnames] -- vhost-style root dispatch by attach name
//   - [WithAttacher] -- full-control attach handling
//   - [WithTracer] -- OpenTelemetry TracerProvider
//   - [WithMeter] -- OpenTelemetry MeterProvider
//   - [WithMiddleware] -- dispatch middleware chain
//
// # Middleware
//
// [Middleware] wraps the dispatch [Handler], enabling cross-cutting concerns
// such as logging, metrics, or access control. When OpenTelemetry providers
// are configured, tracing and metrics middleware is prepended automatically.
//
// # Observability
//
// The server supports OpenTelemetry traces and metrics via the OTel API (no
// SDK dependency). Configure with [WithTracer] and [WithMeter]. Structured
// logging uses [log/slog] with automatic trace ID correlation via
// [NewTraceHandler].
//
// # Sub-packages
//
//   - [github.com/dotwaffle/ninep/server/memfs] -- in-memory file and directory
//     helpers with a fluent builder API
//   - [github.com/dotwaffle/ninep/server/passthrough] -- reference passthrough
//     filesystem using *at syscalls
//   - [github.com/dotwaffle/ninep/server/fstest] -- protocol-level test harness
//     for validating filesystem implementations
//
// # Example
//
// A minimal read-only file server:
//
//	package main
//
//	import (
//	    "context"
//	    "log"
//	    "net"
//
//	    "github.com/dotwaffle/ninep/proto"
//	    "github.com/dotwaffle/ninep/server"
//	)
//
//	type HelloFile struct {
//	    server.Inode
//	}
//
//	func (f *HelloFile) Getattr(_ context.Context, _ proto.AttrMask) (proto.Attr, error) {
//	    return proto.Attr{
//	        Valid: proto.AttrMode | proto.AttrSize,
//	        Mode:  0o444,
//	        Size:  11,
//	    }, nil
//	}
//
//	func (f *HelloFile) Read(_ context.Context, offset uint64, count uint32) ([]byte, error) {
//	    data := []byte("hello world")
//	    if offset >= uint64(len(data)) {
//	        return nil, nil
//	    }
//	    end := offset + uint64(count)
//	    if end > uint64(len(data)) {
//	        end = uint64(len(data))
//	    }
//	    return data[offset:end], nil
//	}
//
//	func main() {
//	    root := &HelloFile{}
//	    root.Init(proto.QID{Type: proto.QTFILE, Path: 1}, root)
//
//	    srv := server.New(root)
//	    ln, err := net.Listen("tcp", ":5640")
//	    if err != nil {
//	        log.Fatal(err)
//	    }
//	    log.Fatal(srv.Serve(context.Background(), ln))
//	}
package server

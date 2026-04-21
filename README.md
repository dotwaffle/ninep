# ninep

[![Go Reference](https://pkg.go.dev/badge/github.com/dotwaffle/ninep.svg)](https://pkg.go.dev/github.com/dotwaffle/ninep)
[![Go Report Card](https://goreportcard.com/badge/github.com/dotwaffle/ninep)](https://goreportcard.com/report/github.com/dotwaffle/ninep)

A Go library implementing the 9P2000.L and 9P2000.u network filesystem
protocols. Provides a capability-based API inspired by
[go-fuse/v2/fs](https://pkg.go.dev/github.com/hanwen/go-fuse/v2/fs) where
implementers embed only the interfaces they need, eliminating boilerplate for
unsupported operations.

## Features

- 9P2000.L (Linux v9fs compatible) and 9P2000.u protocol support
- Capability-based API -- implement only the interfaces you need
- Automatic ENOSYS for unimplemented operations via Inode embedding
- OpenTelemetry traces and metrics (API only, no SDK dependency)
- Structured logging via slog with trace correlation
- Middleware support for cross-cutting concerns
- In-memory filesystem helpers (memfs package)
- Protocol-level test harness (fstest package)
- Reference passthrough filesystem implementation

## Installation

```
go get github.com/dotwaffle/ninep
```

## Quick Start

Define a node type, embed `server.Inode`, and implement the capabilities you
need. The server handles everything else.

```go
package main

import (
	"context"
	"log"
	"net"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

// HelloFile serves a static "hello world" file.
type HelloFile struct {
	server.Inode
}

func (f *HelloFile) Getattr(_ context.Context, _ proto.AttrMask) (proto.Attr, error) {
	return proto.Attr{
		Valid: proto.AttrMode | proto.AttrSize,
		Mode:  0o444,
		Size:  11,
	}, nil
}

func (f *HelloFile) Read(_ context.Context, buf []byte, offset uint64) (int, error) {
	data := []byte("hello world")
	if offset >= uint64(len(data)) {
		return 0, nil
	}
	end := min(offset+uint64(len(buf)), uint64(len(data)))
	return copy(buf, data[offset:end]), nil
}

func main() {
	root := &HelloFile{}
	root.Init(proto.QID{Type: proto.QTFILE, Path: 1}, root)

	srv := server.New(root)
	ln, err := net.Listen("tcp", ":5640")
	if err != nil {
		log.Fatal(err)
	}
	log.Fatal(srv.Serve(context.Background(), ln))
}
```

## Platform support

The library — `proto/`, `server/`, `server/memfs/`, `server/fstest/`,
`internal/bufpool/` — builds and runs on every platform Go supports.

The reference `server/passthrough/` filesystem is **Linux-only**: it uses
`O_PATH`, the `*at` syscall family, and the Linux `Lgetxattr` xattr API
to anchor every node to a specific inode for path-traversal safety. On
darwin / freebsd / windows the package compiles to its godoc only;
`passthrough.NewRoot` is undefined.

To serve 9P from a non-Linux host, write your own `server.Node` types
(the same way you would with `go-fuse`) or use the in-memory `memfs`
helpers.

## Package Layout

| Package | Description |
|---------|-------------|
| `proto/` | Wire types, constants, encoding helpers |
| `proto/p9l/` | 9P2000.L codec (Encode/Decode) |
| `proto/p9u/` | 9P2000.u codec (Encode/Decode) |
| `server/` | Server core, capability interfaces, Inode |
| `server/memfs/` | In-memory file/dir helpers and builder |
| `server/passthrough/` | Host OS passthrough filesystem |
| `server/fstest/` | Protocol-level test harness |

## Testing

```
go test -race ./...
```

## Documentation

- [Getting Started Guide](docs/GETTING-STARTED.md)
- [Architecture & Design](docs/ARCHITECTURE.md)
- [API Reference](docs/API.md)
- [Configuration Reference](docs/CONFIGURATION.md)
- [Development Guide](docs/DEVELOPMENT.md)
- [Testing Guide](docs/TESTING.md)

Full API documentation is available on
[pkg.go.dev](https://pkg.go.dev/github.com/dotwaffle/ninep).

## Protocol References

- [9P2000.L protocol (kernel.org)](https://docs.kernel.org/filesystems/9p.html)
- [Plan 9 manual pages](https://man.cat-v.org/plan_9)

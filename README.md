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
- Capability-based API: implement only the interfaces you need
- Automatic ENOSYS for unimplemented operations via Inode embedding
- Wire-level 9P client with io.Reader/Writer/Closer File handles
- OpenTelemetry traces and metrics (API only, no SDK dependency)
- Structured logging via slog with trace correlation
- Middleware support for cross-cutting concerns
- In-memory filesystem helpers (memfs package)
- Protocol-level test harness (fstest package)
- Reference passthrough filesystem implementation
- AF_VSOCK transport for guest/host virtio-vsock connections

## Installation

```
go get github.com/dotwaffle/ninep
```

## Quick Start

This server exposes one in-memory file. TCP does not provide protocol
authentication: bind loopback as shown, or place the server behind an
authenticated transport before exposing it to a network.

```go
package main

import (
	"context"
	"log"
	"net"

	"github.com/dotwaffle/ninep/server"
	"github.com/dotwaffle/ninep/server/memfs"
)

func main() {
	root := memfs.NewDir(new(server.QIDGenerator)).
		AddStaticFile("hello.txt", "hello world")

	srv, err := server.New(root)
	if err != nil {
		log.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:5640")
	if err != nil {
		log.Fatal(err)
	}
	log.Fatal(srv.Serve(context.Background(), ln))
}
```

The `client` package provides a high-level API for talking to a 9P server. It
handles tag allocation, message framing, and session management:

```go
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
)

func main() {
	ctx := context.Background()

	nc, err := net.Dial("tcp", "localhost:5640")
	if err != nil {
		log.Fatal(err)
	}

	c, err := client.Dial(ctx, nc)
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	if _, err := c.Attach(ctx, "me", "", proto.NoUID); err != nil {
		log.Fatal(err)
	}

	f, err := c.OpenFile(ctx, "hello.txt", os.O_RDONLY, 0)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	content, err := io.ReadAll(f)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Content: %s", string(content))
}
```

## Platform support

The library (`proto/`, `server/`, `server/memfs/`, `server/fstest/`,
`internal/bufpool/`) builds and runs on every platform Go supports.

The reference `server/passthrough/` filesystem supports **Linux and
FreeBSD (14.0+)**: both ports use `O_PATH`, the `*at` syscall family,
and inode-anchored file descriptors to anchor every node to a specific
inode for path-traversal safety. On darwin / windows / other platforms
the package compiles to its godoc only; `passthrough.NewRoot` is
undefined.

To serve 9P from an unsupported host, write your own `server.Node`
types (the same way you would with `go-fuse`) or use the in-memory
`memfs` helpers.

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
| `client/` | 9P client: Conn, File (io.Reader/Writer/Closer), Session |
| `client/clienttest/` | Server/client test pair helpers (mirrors httptest) |
| `vsock/` | AF_VSOCK Listen/Dial for virtio-vsock transport |

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

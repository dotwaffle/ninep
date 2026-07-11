# Getting Started with ninep

`ninep` is a Go library for building 9P2000.L and 9P2000.u servers and clients. Its server-side API is "capability-based": you only implement the interfaces for the operations your filesystem supports.

## Building a Minimal Filesystem

To build a filesystem, you define a struct that embeds `server.Inode`. By embedding `Inode`, your node automatically returns `ENOSYS` for any operation you don't explicitly implement.

Here is a complete server that serves a single read-only file. The protocol
does not implement authentication, so the example binds loopback. Use an
authenticated transport or a trusted network for remote access.

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
	gen := &server.QIDGenerator{}
	root := memfs.NewDir(gen).
		AddStaticFile("hello.txt", "Hello, 9P world!\n")

	s, err := server.New(root)
	if err != nil {
		log.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:5640")
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Serving 9P on %s", l.Addr())
	if err := s.Serve(context.Background(), l); err != nil {
		log.Fatal(err)
	}
}
```

## Using the Go Client

The `client` package provides a high-level API for interacting with 9P servers. It handles tag allocation, message framing, and session management automatically.

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
)

func main() {
	ctx := context.Background()

	// 1. Establish a network connection.
	nc, err := net.Dial("tcp", "localhost:5640")
	if err != nil {
		log.Fatal(err)
	}

	// 2. Wrap it in a 9P client.
	c, err := client.Dial(ctx, nc)
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	// 3. Attach to the root.
	if _, err := c.Attach(ctx, "me", ""); err != nil {
		log.Fatal(err)
	}

	// 4. Open the file we created earlier.
	f, err := c.OpenFile(ctx, "hello.txt", os.O_RDONLY, 0)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	// 5. Read and print the content.
	content, err := io.ReadAll(f)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Content: %s", string(content))
}
```

## Mounting with the Linux Kernel

`ninep` servers are fully compatible with the Linux kernel's `v9fs` client. To mount the example server on Linux:

```bash
# Create a mount point
mkdir /tmp/9p

# Mount via TCP (run as root or use sudo)
sudo mount -t 9p -o trans=tcp,port=5640,version=9p2000.L,msize=65536 127.0.0.1 /tmp/9p

# Access the files
cat /tmp/9p/hello.txt
```

Recommended mount options:
- `trans=tcp`: Use TCP transport.
- `version=9p2000.L`: Use the Linux-optimized protocol extensions.
- `msize=65536`: Match a server memory pool bucket for optimal resource reuse.

## Performance Notes

A few properties worth knowing when sizing buffers or reasoning about throughput:

- **Pooled hot-path requests.** `internal/pool.Cache` recycles structs for the common operations (Read, Write, Walk, Clunk, Setattr, Mkdir, ...) so steady-state dispatch avoids per-request heap allocations.
- **Recv-mutex worker model.** Each connection runs goroutines bounded by `WithMaxInflight`; readers lazy-spawn successors so multiple in-flight requests dispatch concurrently without a dedicated worker pool.
- **Streaming codecs.** Wire encoding works directly against `io.Reader` / `io.Writer` to keep intermediate buffering off the hot path.

## Core Concepts

### Nodes and Inodes
Every entry in your filesystem is a `Node`. For most use cases, you should embed `server.Inode` in your node struct. The `Inode` handles:
- **Default errors**: Returns `ENOSYS` for unimplemented operations.
- **Tree management**: Provides `AddChild`, `RemoveChild`, and a default `Lookup` implementation.

### Capability Interfaces
`ninep` defines fine-grained interfaces in `server/node.go` for each 9P operation. Common ones include:
- `NodeLookuper`: Resolve a child by name (if not using `Inode` tree management).
- `NodeOpener`: Handle file open flags.
- `NodeReader` / `NodeWriter`: Data I/O.
- `NodeGetattrer`: Return file attributes (stat).

### File Handles
For stateful I/O (like tracking an OS file descriptor), `NodeOpener.Open` can return a `FileHandle`. If the returned handle implements `FileReader` or `FileWriter`, those methods will be used for subsequent I/O on that specific open instance.

## Next Steps
- Explore the [API Reference](API.md) for a full list of capability interfaces.
- Check [ARCHITECTURE.md](ARCHITECTURE.md) to understand the server's high-performance concurrent model.
- See [CONFIGURATION.md](CONFIGURATION.md) for server options like message size limits and OTel integration.

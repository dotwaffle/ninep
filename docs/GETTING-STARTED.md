<!-- generated-by: gsd-doc-writer -->
# Getting Started with ninep

This guide walks you through building 9P filesystems with ninep, from a
single static file to a directory tree served over TCP that you can mount
with the Linux kernel client.

## Prerequisites

- **Go >= 1.24** (the module declares `go 1.26`; Go 1.24+ is the minimum
  that supports the `tool` directive and `testing/synctest`)
- **Linux** for mounting with `mount -t 9p` (the library compiles on any
  OS, but the v9fs mount test requires Linux)
- A working `$GOPATH` or Go modules environment

## Installation

```bash
go get github.com/dotwaffle/ninep@latest
```

The only runtime dependency is the OpenTelemetry API (`go.opentelemetry.io/otel`).
No SDK is pulled in -- that is an application-level concern.

## First Filesystem: A Static File

The smallest useful filesystem is a single read-only file. Create a struct
that embeds `server.Inode` and implement `NodeReader` and `NodeGetattrer`.

```go
package main

import (
	"context"
	"log"
	"net"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

// HelloFile serves a read-only file with static content.
type HelloFile struct {
	server.Inode
}

func (f *HelloFile) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return nil, 0, nil
}

func (f *HelloFile) Read(_ context.Context, offset uint64, count uint32) ([]byte, error) {
	data := []byte("hello world\n")
	if offset >= uint64(len(data)) {
		return nil, nil
	}
	end := offset + uint64(count)
	if end > uint64(len(data)) {
		end = uint64(len(data))
	}
	return data[offset:end], nil
}

func (f *HelloFile) Getattr(_ context.Context, _ proto.AttrMask) (proto.Attr, error) {
	return proto.Attr{
		Mode:  0o444,
		Size:  12,
		NLink: 1,
	}, nil
}

func main() {
	var gen server.QIDGenerator

	root := &HelloFile{}
	root.Init(gen.Next(proto.QTFILE), root)

	srv := server.New(root)

	ln, err := net.Listen("tcp", ":5640")
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("serving on %s", ln.Addr())
	log.Fatal(srv.Serve(context.Background(), ln))
}
```

Key points:

- **`server.Inode`** is embedded in every node struct. It provides default
  ENOSYS responses for every capability interface you do not implement.
- **`Inode.Init(qid, self)`** must be called once per node to set the QID
  and the back-reference. Use `QIDGenerator.Next(type)` for monotonically
  increasing, unique QID paths.
- **`server.New(root, ...Option)`** creates the server. The root node is
  what gets returned on `Tattach`.
- **`Server.Serve(ctx, ln)`** accepts connections and blocks until the
  context is cancelled or the listener errors.

## Adding a Directory with Children

A directory node implements `NodeLookuper` so the server can resolve walk
requests. The `Inode` tree manages parent/child relationships via
`AddChild` and provides a default `Lookup` implementation.

```go
package main

import (
	"context"
	"log"
	"net"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
)

// Dir is a read-only directory that serves entries from its Inode children.
type Dir struct {
	server.Inode
}

func (d *Dir) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return nil, 0, nil
}

func (d *Dir) Readdir(_ context.Context) ([]proto.Dirent, error) {
	children := d.Children()
	entries := make([]proto.Dirent, 0, len(children))
	var offset uint64
	for name, inode := range children {
		qid := inode.QID()
		offset++
		entries = append(entries, proto.Dirent{
			QID:    qid,
			Offset: offset,
			Type:   uint8(qid.Type),
			Name:   name,
		})
	}
	return entries, nil
}

func (d *Dir) Getattr(_ context.Context, _ proto.AttrMask) (proto.Attr, error) {
	children := d.Children()
	return proto.Attr{
		Mode:  0o040755,
		NLink: uint64(2 + len(children)),
	}, nil
}

// StaticFile is a read-only file with immutable content.
type StaticFile struct {
	server.Inode
	content []byte
}

func (f *StaticFile) Open(_ context.Context, _ uint32) (server.FileHandle, uint32, error) {
	return nil, 0, nil
}

func (f *StaticFile) Read(_ context.Context, offset uint64, count uint32) ([]byte, error) {
	if offset >= uint64(len(f.content)) {
		return nil, nil
	}
	end := offset + uint64(count)
	if end > uint64(len(f.content)) {
		end = uint64(len(f.content))
	}
	out := make([]byte, end-offset)
	copy(out, f.content[offset:end])
	return out, nil
}

func (f *StaticFile) Getattr(_ context.Context, _ proto.AttrMask) (proto.Attr, error) {
	return proto.Attr{
		Mode:  0o444,
		Size:  uint64(len(f.content)),
		NLink: 1,
	}, nil
}

func main() {
	var gen server.QIDGenerator

	// Build the tree: root/ -> greeting.txt, info.txt
	root := &Dir{}
	root.Init(gen.Next(proto.QTDIR), root)

	greeting := &StaticFile{content: []byte("hello world\n")}
	greeting.Init(gen.Next(proto.QTFILE), greeting)
	root.AddChild("greeting.txt", greeting.EmbeddedInode())

	info := &StaticFile{content: []byte("ninep getting started\n")}
	info.Init(gen.Next(proto.QTFILE), info)
	root.AddChild("info.txt", info.EmbeddedInode())

	srv := server.New(root)
	ln, err := net.Listen("tcp", ":5640")
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("serving on %s", ln.Addr())
	log.Fatal(srv.Serve(context.Background(), ln))
}
```

The `Inode` provides a default `Lookup` implementation that searches its
children map. You only need to override `Lookup` if your directory needs
custom resolution logic (lazy loading, external data sources, etc.).

## Serving over TCP

Both examples above already listen on TCP port 5640, the conventional 9P
port. You can use any `net.Listener`:

```go
// TCP
ln, _ := net.Listen("tcp", ":5640")

// Unix socket
ln, _ := net.Listen("unix", "/tmp/ninep.sock")

// Serve a single connection directly
conn, _ := ln.Accept()
srv.ServeConn(ctx, conn)
```

`Server.Serve(ctx, ln)` spawns a goroutine per connection and blocks until
the context is cancelled. For single-connection scenarios (e.g., virtio-vsock),
use `Server.ServeConn(ctx, conn)` directly.

## Testing with the Linux v9fs Client

Once your server is running, mount it on Linux:

```bash
# Mount the filesystem (as root)
sudo mount -t 9p -o trans=tcp,port=5640,version=9p2000.L,msize=65536 \
    127.0.0.1 /mnt/9p

# List files
ls /mnt/9p

# Read a file
cat /mnt/9p/greeting.txt

# Unmount
sudo umount /mnt/9p
```

For Unix sockets:

```bash
sudo mount -t 9p -o trans=unix,version=9p2000.L \
    /tmp/ninep.sock /mnt/9p
```

Common mount options:

| Option | Description |
|--------|-------------|
| `trans=tcp` | TCP transport (default) |
| `trans=unix` | Unix domain socket transport |
| `port=5640` | TCP port (default: 564) |
| `version=9p2000.L` | Protocol version (9P2000.L or 9P2000.u) |
| `msize=65536` | Maximum message size in bytes |
| `cache=none` | Disable client-side caching |
| `access=user` | Per-user access control |

## Using memfs for Quick Prototyping

The `server/memfs` package provides ready-made in-memory node types and a
fluent builder API. Use it when you want a working filesystem tree without
implementing custom node types.

### Node Types

| Type | Description |
|------|-------------|
| `memfs.MemFile` | Read-write in-memory file (thread-safe) |
| `memfs.MemDir` | In-memory directory with Create, Mkdir, Unlink |
| `memfs.StaticFile` | Read-only file with string content |

### Builder API

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
	var gen server.QIDGenerator

	root := memfs.NewDir(&gen).
		AddStaticFile("version", "1.0.0").
		AddFile("config.json", []byte(`{"debug": true}`)).
		AddFileWithMode("run.sh", []byte("#!/bin/sh\necho hi"), 0o755).
		WithDir("data", func(d *memfs.MemDir) {
			d.AddFile("cache.db", nil).
				AddStaticFile("readme.txt", "data directory")
		}).
		AddSymlink("latest", "data/cache.db")

	srv := server.New(root)
	ln, err := net.Listen("tcp", ":5640")
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("serving on %s", ln.Addr())
	log.Fatal(srv.Serve(context.Background(), ln))
}
```

Builder methods return the parent directory for chaining:

| Method | Description |
|--------|-------------|
| `NewDir(gen)` | Create a root MemDir |
| `AddFile(name, data)` | Add a read-write file (mode 0644) |
| `AddFileWithMode(name, data, mode)` | Add a read-write file with custom mode |
| `AddStaticFile(name, content)` | Add a read-only file (mode 0444) |
| `AddDir(name)` | Add an empty subdirectory |
| `WithDir(name, fn)` | Add a subdirectory and populate it via callback |
| `SubDir(name)` | Retrieve an existing child directory for further construction |
| `AddSymlink(name, target)` | Add a symbolic link |

### Dynamic Operations

`MemDir` implements `NodeCreater`, `NodeMkdirer`, and `NodeUnlinker`, so
clients can create files and directories, and remove entries at runtime.
`MemFile` implements `NodeWriter` and `NodeSetattrer`, so clients can
write data and modify attributes.

## Using fstest to Validate Your Implementation

The `server/fstest` package provides a protocol-level test harness that
exercises your filesystem against the 9P2000.L contract. It tests walk
resolution, read/write operations, directory listing, file creation,
attribute retrieval, error handling, and concurrent access.

### Required Tree Shape

Your root node must expose the following tree:

```
root/
  file.txt       (content: "hello world")
  empty          (content: "")
  sub/
    nested.txt   (content: "nested content")
```

### Running the Harness

```go
package myfs_test

import (
	"testing"

	"github.com/dotwaffle/ninep/server"
	"github.com/dotwaffle/ninep/server/fstest"
	"github.com/dotwaffle/ninep/server/memfs"
)

func TestMyFS(t *testing.T) {
	var gen server.QIDGenerator

	root := memfs.NewDir(&gen).
		AddFile("file.txt", []byte("hello world")).
		AddFile("empty", []byte("")).
		WithDir("sub", func(d *memfs.MemDir) {
			d.AddFile("nested.txt", []byte("nested content"))
		})

	fstest.Check(t, root)
}
```

`fstest.Check(t, root)` runs all registered test cases as subtests. The
test cases include:

| Category | Cases |
|----------|-------|
| Walk | root attach, child walk, deep walk, nonexistent, clone |
| Read/Write | basic read, offset read, past-EOF, basic write |
| Directory | readdir basic, readdir empty |
| Create/Mkdir | file creation, directory creation |
| Attributes | file getattr, directory getattr |
| Errors | walk from file (ENOTDIR), read on directory |
| Unlink | file removal |
| Concurrency | concurrent reads |

### Factory-Based Harness

If your filesystem holds OS-level resources that get cleaned up when the
server connection closes (like the `passthrough` package), use
`CheckFactory` to create a fresh root per test case:

```go
func TestPassthrough(t *testing.T) {
	fstest.CheckFactory(t, func(t *testing.T) server.Node {
		root, err := passthrough.NewRoot("/path/to/test/dir")
		if err != nil {
			t.Fatal(err)
		}
		return root
	})
}
```

### Selective Test Execution

The `fstest.Cases` slice is exported, so you can filter or run individual
cases:

```go
func TestWalkOnly(t *testing.T) {
	var gen server.QIDGenerator
	root := fstest.NewTestTree(&gen)

	for _, tc := range fstest.Cases {
		if strings.HasPrefix(tc.Name, "walk/") {
			t.Run(tc.Name, func(t *testing.T) {
				tc.Run(t, root)
			})
		}
	}
}
```

`fstest.NewTestTree(gen)` is a convenience function that builds the
required tree shape using internal test node types (no memfs dependency).

## Server Options

`server.New` accepts functional options to configure behavior:

| Option | Default | Description |
|--------|---------|-------------|
| `WithMaxMsize(n)` | 131072 (128KB) | Maximum message size during version negotiation |
| `WithMaxInflight(n)` | 64 | Maximum concurrent in-flight requests per connection |
| `WithLogger(logger)` | `slog.Default()` with trace correlation | Structured logger (automatically wrapped with `NewTraceHandler`) |
| `WithIdleTimeout(d)` | 0 (disabled) | Per-connection idle timeout |
| `WithAnames(map)` | nil | Map of aname strings to root nodes for vhost-style dispatch |
| `WithAttacher(a)` | nil | Custom attach handler (overrides root and anames) |
| `WithMiddleware(mw...)` | nil | Request middleware chain |
| `WithTracer(tp)` | nil | OpenTelemetry TracerProvider for per-operation spans |
| `WithMeter(mp)` | nil | OpenTelemetry MeterProvider for metrics |

## Common Setup Issues

**Port already in use** -- If port 5640 is taken, choose a different port
and pass `port=NNNN` to the mount command.

**"version not negotiated" errors** -- Ensure the client sends a `Tversion`
message before any other operation. The Linux v9fs client does this
automatically, but custom clients must negotiate first.

**Mount fails with "Protocol not supported"** -- Verify the kernel has 9P
support: `modprobe 9p && modprobe 9pnet_fd`. Check `version=9p2000.L` in
the mount options.

**Files appear empty or Getattr returns wrong size** -- Your `Getattr`
implementation must return the correct `Size` field. The kernel client uses
this to determine how much data to read.

**ENOSYS on every operation** -- You forgot to call `Init(qid, self)` on
your node, or your methods are defined on a value receiver instead of a
pointer receiver, so they do not satisfy the capability interfaces.

## Next Steps

- [ARCHITECTURE.md](ARCHITECTURE.md) -- System design and component overview
- [DEVELOPMENT.md](DEVELOPMENT.md) -- Local development setup and build commands
- [TESTING.md](TESTING.md) -- Test framework details and coverage
- [pkg.go.dev](https://pkg.go.dev/github.com/dotwaffle/ninep) -- Full API reference

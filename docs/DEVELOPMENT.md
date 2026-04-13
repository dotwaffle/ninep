<!-- generated-by: gsd-doc-writer -->
# Development Guide

This document covers everything needed to contribute to ninep: environment setup, building, testing, coding conventions, and how to extend the server with new 9P operations or middleware.

## Prerequisites

- **Go >= 1.26** -- the module uses `go 1.26` in `go.mod`
- **golangci-lint** -- for linting (install via `go install` or your package manager)
- **Linux** -- required for the `server/passthrough` package tests (uses `golang.org/x/sys/unix` and `*at` syscalls); all other packages are platform-independent

## Building

ninep is a library, not a binary. There is no build step beyond ensuring dependencies are fetched:

```bash
go mod download
```

Verify everything compiles:

```bash
go build ./...
```

## Running Tests

Full test suite with race detection:

```bash
go test -race -count=1 ./...
```

Run a specific package:

```bash
go test -race ./server/...
go test -race ./proto/...
go test -race ./server/memfs/...
go test -race ./server/passthrough/...
go test -race ./server/fstest/...
```

Run a single test:

```bash
go test -race -run TestMiddlewareChainOrdering ./server/
```

The `proto/p9l` and `proto/p9u` packages include fuzz tests. Run them with:

```bash
go test -fuzz=FuzzDecode ./proto/p9l/
go test -fuzz=FuzzDecode ./proto/p9u/
```

## Project Structure

```
ninep/
  go.mod
  proto/              Wire types, constants, encoding helpers
    constants.go        HeaderSize, NoFid, NoTag, QIDSize
    types.go            QID, Attr, Dirent, FSStat, Fid, Tag, etc.
    message.go          Message interface, MessageType enum
    messages.go         Shared T/R message types (Tversion, Twalk, etc.)
    encode.go           WriteUint32, WriteString, WriteQID, etc.
    decode.go           ReadUint32, ReadString, ReadQID, etc.
    errno.go            Errno type and POSIX error constants
    p9l/                9P2000.L codec
      codec.go            Encode/Decode for 9P2000.L framing
      messages.go         9P2000.L-specific message types (Tlopen, Tgetattr, etc.)
    p9u/                9P2000.u codec
      codec.go            Encode/Decode for 9P2000.u framing
      messages.go         9P2000.u-specific message types (Topen, Tstat, etc.)
  server/             Server core, capability interfaces, Inode
    node.go             Capability interfaces (NodeReader, NodeWriter, etc.)
    inode.go            Inode with ENOSYS defaults for all capabilities
    server.go           Server struct, New(), Serve(), ServeConn()
    conn.go             Per-connection lifecycle: read loop, write loop, version negotiation
    dispatch.go         Message routing to bridge handlers
    bridge.go           Bridge handlers (handleLopen, handleRead, handleWrite, etc.)
    fid.go              Fid table with lifecycle state tracking
    flush.go            Inflight request tracking and Tflush cancellation
    cleanup.go          Connection shutdown: cancel, drain, clunk, close
    middleware.go       Handler/Middleware types, chain(), WithMiddleware()
    options.go          Functional options (WithMaxMsize, WithLogger, etc.)
    errors.go           Sentinel errors (ErrFidInUse, ErrNotNegotiated, etc.)
    filehandle.go       FileHandle, FileReader, FileWriter, FileReleaser interfaces
    composable.go       ReadOnlyFile, ReadOnlyDir, Symlink, Device, StaticFS helpers
    helpers.go          Symlink, Device, StaticFS constructors (SymlinkTo, DeviceNode, etc.)
    qid.go              QIDGenerator and PathQID helper
    context.go          ConnInfo and ConnFromContext
    dirent.go           EncodeDirents helper
    otel.go             OpenTelemetry middleware and connection-level instruments
    logging.go          NewTraceHandler (slog + OTel correlation), NewLoggingMiddleware
    memfs/              In-memory file/directory helpers
      memfs.go            MemFile, MemDir, StaticFile types
      builder.go          Fluent builder API (NewDir, AddFile, WithDir, etc.)
    passthrough/        Host OS passthrough filesystem (Linux only)
      passthrough.go      Root and Node types, NewRoot constructor
      dir.go              Directory operations via *at syscalls
      handle.go           Per-open file handle
      stat.go             Getattr/Setattr via fstatat/utimensat
      lock.go             POSIX byte-range locking
      xattr.go            Extended attribute operations
      uid.go              UIDMapper interface for UID/GID translation
    fstest/             Protocol-level test harness
      fstest.go           Check(), CheckFactory(), test infrastructure
      cases.go            Standard test cases against expected tree shape
```

## Coding Conventions

### Capability Interfaces

The core pattern in ninep: define small, single-method interfaces in `server/node.go`. Each interface maps to one 9P operation. Nodes implement only the interfaces they need; `*Inode` provides ENOSYS defaults for everything else.

```go
// In server/node.go
type NodeFsyncer interface {
    Fsync(ctx context.Context, datasync bool) error
}
```

Interface naming convention: `Node` + operation name + `er` suffix. Examples from the codebase:

| Interface | Method | 9P Operation |
|-----------|--------|--------------|
| `NodeReader` | `Read(ctx, offset, count)` | Tread |
| `NodeWriter` | `Write(ctx, data, offset)` | Twrite |
| `NodeOpener` | `Open(ctx, flags)` | Tlopen |
| `NodeGetattrer` | `Getattr(ctx, mask)` | Tgetattr |
| `NodeReaddirer` | `Readdir(ctx)` | Treaddir |
| `NodeCreater` | `Create(ctx, name, flags, mode, gid)` | Tlcreate |
| `NodeMkdirer` | `Mkdir(ctx, name, mode, gid)` | Tmkdir |
| `NodeLookuper` | `Lookup(ctx, name)` | Twalk (per element) |
| `NodeUnlinker` | `Unlink(ctx, name, flags)` | Tunlinkat |
| `NodeRenamer` | `Rename(ctx, oldName, newDir, newName)` | Trenameat |
| `NodeStatFSer` | `StatFS(ctx)` | Tstatfs |
| `NodeLocker` | `Lock(...)` / `GetLock(...)` | Tlock / Tgetlock |

### Inode Embedding

Nodes embed `*Inode` (via `server.Inode` struct embedding) and call `Init` during construction:

```go
type MyFile struct {
    server.Inode
}

func NewMyFile(gen *server.QIDGenerator) *MyFile {
    f := &MyFile{}
    f.Init(gen.Next(proto.QTFILE), f)
    return f
}
```

Key points:
- `Init(qid, self)` sets the QID and the back-reference so the Inode tree resolves to your struct (not the embedded `*Inode`)
- `Inode` implements all capability interfaces with ENOSYS returns -- compile-time assertions in `inode.go` enforce this
- Override by implementing the capability interface on your struct; the bridge uses type assertions to detect your implementation at runtime

### Functional Options

The server uses the `Option` pattern defined in `server/options.go`:

```go
srv := server.New(root,
    server.WithMaxMsize(1 << 20),       // 1MB max message size
    server.WithMaxInflight(128),         // 128 concurrent requests
    server.WithLogger(slog.Default()),   // structured logger
    server.WithIdleTimeout(30*time.Second),
    server.WithTracer(tp),               // OTel TracerProvider
    server.WithMeter(mp),                // OTel MeterProvider
    server.WithMiddleware(myMiddleware), // custom middleware
)
```

Defaults (from `server.go` `New` function):
- `maxMsize`: 131072 (128KB)
- `maxInflight`: 64
- `logger`: `slog.Default()` wrapped with `NewTraceHandler` for trace correlation
- `idleTimeout`: 0 (no timeout)

### Error Wrapping

Errors use `fmt.Errorf` with `%w` for wrapping. Protocol errors are `proto.Errno` values (e.g., `proto.ENOENT`, `proto.ENOSYS`, `proto.EIO`). The bridge converts Go errors to protocol errors via `errnoFromError()` in `dispatch.go`, which uses `errors.As` to extract a `proto.Errno` and falls back to `EIO`.

Sentinel errors for internal server logic live in `server/errors.go`:

```go
var (
    ErrFidInUse      = errors.New("fid already in use")
    ErrFidNotFound   = errors.New("fid not found")
    ErrNotNegotiated = errors.New("version not negotiated")
    ErrMsizeTooSmall = errors.New("msize too small")
    ErrNotDirectory  = errors.New("not a directory")
)
```

When returning errors from node implementations, wrap `proto.Errno` values:

```go
return fmt.Errorf("lookup %q: %w", name, proto.ENOENT)
```

### Table-Driven Tests

All tests use table-driven style with `t.Parallel()`. Example from `middleware_test.go`:

```go
func TestIsErrorResponse(t *testing.T) {
    t.Parallel()

    tests := []struct {
        name string
        msg  proto.Message
        want bool
    }{
        {name: "Rlerror is error", msg: &p9l.Rlerror{Ecode: proto.EIO}, want: true},
        {name: "Rversion is not error", msg: &proto.Rversion{}, want: false},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            t.Parallel()
            got := isErrorResponse(tt.msg)
            if got != tt.want {
                t.Fatalf("isErrorResponse(%T): got %v, want %v", tt.msg, got, tt.want)
            }
        })
    }
}
```

Testing conventions:
- Use `net.Pipe()` for in-memory client-server tests (no real sockets)
- Use `t.Cleanup` for teardown
- No testify -- use stdlib `testing` with `t.Helper()` and direct comparisons
- No mocking frameworks -- implement single-method interfaces inline in tests

### FileHandle Pattern

`NodeOpener.Open` returns a `FileHandle` to carry per-open state. If the handle implements `FileReader`, `FileWriter`, or `FileReaddirer`, those take priority over the Node-level methods for that open instance. This enables stateful I/O without polluting the node:

```go
type myHandle struct {
    offset int64
}

func (h *myHandle) Read(ctx context.Context, offset uint64, count uint32) ([]byte, error) {
    // per-open state
}

func (n *myNode) Open(ctx context.Context, flags uint32) (server.FileHandle, uint32, error) {
    return &myHandle{}, 0, nil
}
```

The bridge dispatch priority is: FileHandle interface > Node interface > ENOSYS.

## Adding a New 9P Operation

This section walks through the full flow of adding a new capability. Use `NodeFsyncer` as a hypothetical example.

### Step 1: Define the Capability Interface

In `server/node.go`, add the interface:

```go
// NodeFsyncer is implemented by nodes that support fsync.
type NodeFsyncer interface {
    Fsync(ctx context.Context, datasync bool) error
}
```

Follow existing naming: `Node` + verb + `er`. First parameter is always `context.Context`.

### Step 2: Add the ENOSYS Default to Inode

In `server/inode.go`, add a default method that returns `proto.ENOSYS`:

```go
// Fsync returns proto.ENOSYS. Override by implementing NodeFsyncer.
func (i *Inode) Fsync(_ context.Context, _ bool) error {
    return proto.ENOSYS
}
```

Add a compile-time assertion at the top of `inode.go`:

```go
var (
    _ NodeFsyncer = (*Inode)(nil)
    // ... existing assertions ...
)
```

### Step 3: Define the Wire Message Type

If the message type does not already exist in `proto/`, add it. For 9P2000.L messages, add the T and R structs in `proto/p9l/messages.go`:

```go
type Tfsync struct {
    Fid      proto.Fid
    Datasync uint32
}

type Rfsync struct{}
```

Implement `Message` interface methods (`Type()`, `EncodeTo()`, `DecodeFrom()`) on both.

Add the `MessageType` constants in `proto/message.go` and register the type in `conn.go` `newMessage()`:

```go
case proto.TypeTfsync:
    return &p9l.Tfsync{}, nil
```

### Step 4: Add the Dispatch Case

In `server/dispatch.go`, add a case to the `dispatch()` switch:

```go
case *p9l.Tfsync:
    return c.handleFsync(ctx, m)
```

### Step 5: Write the Bridge Handler

In `server/bridge.go`, add the handler:

```go
func (c *conn) handleFsync(ctx context.Context, m *p9l.Tfsync) proto.Message {
    fs := c.fids.get(m.Fid)
    if fs == nil {
        return c.errorMsg(proto.EBADF)
    }

    fsyncer, ok := fs.node.(NodeFsyncer)
    if !ok {
        return c.errorMsg(proto.ENOSYS)
    }

    if err := fsyncer.Fsync(ctx, m.Datasync != 0); err != nil {
        return c.errorMsg(errnoFromError(err))
    }

    return &p9l.Rfsync{}
}
```

All bridge handlers follow this pattern:
1. Look up the fid from the fid table (`c.fids.get`)
2. Check fid state if needed (e.g., `fs.state != fidOpened` for operations requiring an open fid)
3. Type-assert to the capability interface
4. Return `ENOSYS` if not implemented, `EBADF` if fid is invalid
5. Call the interface method, convert errors with `errnoFromError()`
6. Return the response message

### Step 6: Update fidFromMessage (middleware support)

In `server/middleware.go`, add a case to `fidFromMessage()` so middleware can extract the fid:

```go
case *p9l.Tfsync:
    return m.Fid, true
```

### Step 7: Write Tests

Add tests in `server/bridge_test.go` using the existing test node infrastructure. Tests embed `Inode`, implement the new interface, set up a server with `net.Pipe()`, and exercise the protocol flow.

If the operation should be covered by the conformance harness, add a case to `server/fstest/cases.go`.

## Adding New Middleware

Middleware wraps the dispatch `Handler` type. The signature is:

```go
type Handler func(ctx context.Context, tag proto.Tag, msg proto.Message) proto.Message
type Middleware func(next Handler) Handler
```

### Writing a Middleware

```go
func NewAccessLogMiddleware(logger *slog.Logger) server.Middleware {
    return func(next server.Handler) server.Handler {
        return func(ctx context.Context, tag proto.Tag, msg proto.Message) proto.Message {
            // Pre-dispatch: inspect the request
            logger.Info("request", slog.String("type", msg.Type().String()))

            // Call the next handler in the chain
            resp := next(ctx, tag, msg)

            // Post-dispatch: inspect the response
            if server.IsErrorResponse(resp) {
                logger.Warn("error response", slog.String("type", msg.Type().String()))
            }

            return resp
        }
    }
}
```

Note: `isErrorResponse` is unexported in the `server` package. Middleware defined within the `server` package can use it directly. External middleware should check the response type directly (e.g., type-assert to `*p9l.Rlerror`).

### Registering Middleware

Pass middleware via `WithMiddleware()`. The first middleware added is outermost (first to execute, last to see the response):

```go
srv := server.New(root,
    server.WithMiddleware(
        mwA,  // outermost: executes first
        mwB,  // inner: executes second
    ),
)
```

Execution order: `mwA-before -> mwB-before -> dispatch -> mwB-after -> mwA-after`.

Middleware can short-circuit by not calling `next`:

```go
func denyAll(next server.Handler) server.Handler {
    return func(ctx context.Context, tag proto.Tag, msg proto.Message) proto.Message {
        return &p9l.Rlerror{Ecode: proto.EPERM}  // never calls next
    }
}
```

### Built-in Middleware

- **OTel middleware** -- automatically prepended when `WithTracer()` or `WithMeter()` is configured. Creates a span per 9P operation with `rpc.system.name=9p`, records duration histograms, request/response sizes, and active request counts. Defined in `server/otel.go`.
- **Logging middleware** -- `NewLoggingMiddleware(logger)` logs each request at Debug level with op name, duration, and error status. Defined in `server/logging.go`.

## Linting

The project uses golangci-lint. Run:

```bash
golangci-lint run ./...
```

No `.golangci.yml` configuration file is present -- the default golangci-lint configuration is used. The standard Go toolchain checks also apply:

```bash
go vet ./...
gofmt -l .
```

## Useful Helpers

| Helper | Location | Purpose |
|--------|----------|---------|
| `QIDGenerator` | `server/qid.go` | Monotonically increasing QID path values (atomic, concurrent-safe) |
| `PathQID()` | `server/qid.go` | Deterministic QID from a path string via FNV-1a hashing |
| `EncodeDirents()` | `server/dirent.go` | Pack `[]proto.Dirent` into wire bytes within a size limit |
| `ConnFromContext()` | `server/context.go` | Access `ConnInfo` (protocol, msize, remote addr) from node handlers |
| `NewTraceHandler()` | `server/logging.go` | Wrap a `slog.Handler` with OTel trace/span ID injection |
| `SymlinkTo()` | `server/helpers.go` | Create a symlink node from a QIDGenerator and target path |
| `DeviceNode()` | `server/helpers.go` | Create a device node with major/minor numbers |
| `StaticStatFS()` | `server/helpers.go` | Create a node returning fixed filesystem statistics |

## Sub-Package Reference

### server/memfs

In-memory filesystem types with a fluent builder API for constructing file trees:

```go
gen := &server.QIDGenerator{}
root := memfs.NewDir(gen).
    AddFile("config.json", configData).
    AddStaticFile("version", "1.0.0").
    WithDir("sub", func(d *memfs.MemDir) {
        d.AddFile("nested.txt", []byte("content"))
    })
```

Types: `MemFile` (read-write), `MemDir` (read-write directory), `StaticFile` (read-only).

### server/passthrough

Reference passthrough filesystem that delegates to the host OS using `*at` syscalls (`openat`, `fstatat`, `readlinkat`, etc.). Linux only. Requires `golang.org/x/sys/unix`.

### server/fstest

Protocol-level test harness. Call `fstest.Check(t, root)` to run the standard conformance suite against any root `Node`. The root must contain the expected tree shape documented in `fstest.ExpectedTree`:

```
root/
  file.txt       (content: "hello world")
  empty           (content: "")
  sub/
    nested.txt   (content: "nested content")
```

For filesystem implementations with OS-level resources, use `fstest.CheckFactory(t, newRootFn)` to get a fresh root per test case.

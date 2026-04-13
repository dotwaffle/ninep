<!-- generated-by: gsd-doc-writer -->
# Architecture

## System Overview

ninep is a Go library implementing the 9P2000.L and 9P2000.u network filesystem protocols. It accepts raw TCP or Unix socket connections, negotiates a protocol dialect, then dispatches incoming 9P messages to user-defined filesystem nodes through a capability-based interface inspired by go-fuse/v2/fs. The primary architectural style is layered: a wire encoding layer (`proto/`), protocol-specific codecs (`proto/p9l/`, `proto/p9u/`), and a server core (`server/`) that bridges protocol messages to filesystem operations. The library depends on the OpenTelemetry API for observability and the Go standard library for everything else.

## Component Diagram

```
                      net.Listener
                           │
                     ┌─────▼─────┐
                     │  Server    │  Accepts connections, holds config
                     └─────┬─────┘
                           │ goroutine-per-connection
                     ┌─────▼─────┐
                     │   conn     │  Version negotiation, read loop,
                     │            │  write loop, fid table, inflight map
                     └──┬──────┬─┘
                        │      │
          ┌─────────────▼─┐  ┌▼──────────────┐
          │  readLoop      │  │  writeLoop     │
          │  (decode +     │  │  (single       │
          │   dispatch)    │  │   writer)      │
          └───────┬────────┘  └───────▲───────┘
                  │                   │
                  │  goroutine-per-request
          ┌───────▼────────┐          │
          │ handleRequest  │──────────┘
          │  (middleware →  │  sends taggedResponse
          │   dispatch →   │  via channel
          │   bridge)      │
          └───────┬────────┘
                  │
          ┌───────▼────────┐
          │  Node (user)   │  Capability interfaces
          │  embed *Inode  │  (NodeReader, NodeWriter, ...)
          └────────────────┘

Wire encoding:
  proto/       ── shared types, Message interface, encode/decode helpers
  proto/p9l/   ── 9P2000.L codec (Encode/Decode)
  proto/p9u/   ── 9P2000.u codec (Encode/Decode)
```

## Package Responsibilities

```
ninep/
├── proto/                 Wire types, Message interface, encoding helpers
│   ├── p9l/               9P2000.L codec and message structs
│   └── p9u/               9P2000.u codec and message structs
└── server/                Server core, dispatch, capability bridge
    ├── memfs/             In-memory node types with fluent builder API
    ├── passthrough/       Host-filesystem passthrough using *at syscalls
    └── fstest/            Protocol-level conformance test harness
```

### `proto/`

Defines the shared vocabulary for all 9P communication:

- **`Message` interface** (`message.go`) -- `Type()`, `EncodeTo(io.Writer)`, `DecodeFrom(io.Reader)`. Every T-message and R-message implements this. The interface handles the body only; the 7-byte header (size[4] + type[1] + tag[2]) is managed by the codec layer.
- **Wire types** (`types.go`) -- `QID`, `Fid`, `Tag`, `Attr`, `AttrMask`, `SetAttr`, `Dirent`, `FSStat`, `FileMode`, lock types.
- **Encoding helpers** (`encode.go`, `decode.go`) -- `WriteUint32`, `ReadUint32`, `WriteString`, `ReadString`, etc. These use `encoding/binary.LittleEndian` directly for zero-allocation hot paths.
- **Shared messages** (`messages.go`) -- `Tversion`/`Rversion`, `Tattach`/`Rattach`, `Twalk`/`Rwalk`, `Tread`/`Rread`, `Twrite`/`Rwrite`, `Tclunk`/`Rclunk`, `Tflush`/`Rflush`, and other base protocol messages used by both dialects.
- **Errno** (`errno.go`) -- `Errno` type implementing `error`, with constants for standard POSIX error numbers (`ENOENT`, `EIO`, `ENOSYS`, etc.).
- **Constants** (`constants.go`) -- `HeaderSize` (7), `MaxWalkElements` (16), `QIDSize` (13), `NoTag`, `NoFid`.

### `proto/p9l/`

9P2000.L codec for Linux kernel v9fs. Provides top-level `Encode(io.Writer, Tag, Message)` and `Decode(io.Reader)` functions. Defines all 9P2000.L-specific message structs: `Tlopen`/`Rlopen`, `Tgetattr`/`Rgetattr`, `Treaddir`/`Rreaddir`, `Tlock`/`Rlock`, `Txattrwalk`/`Rxattrwalk`, and others.

### `proto/p9u/`

9P2000.u codec for Unix extensions. Same `Encode`/`Decode` API shape as p9l. Defines `Topen`/`Ropen`, `Tstat`/`Rstat`, `Rerror` (string-based errors with errno), and other 9P2000.u-specific messages.

### `server/`

The server core. Key files and their roles:

| File | Responsibility |
|------|---------------|
| `server.go` | `Server` struct, `New()` constructor, `Serve()` accept loop, `ServeConn()` |
| `conn.go` | `conn` struct, version negotiation, `readLoop`, `writeLoop`, `codec` abstraction |
| `dispatch.go` | `dispatch()` type-switch routing, `handleAttach`, `handleWalk`, `handleClunk`, `handleFlush` |
| `bridge.go` | Capability bridge handlers: `handleLopen`, `handleRead`, `handleWrite`, `handleGetattr`, `handleSetattr`, `handleReaddir`, `handleLcreate`, `handleMkdir`, `handleSymlink`, `handleLink`, `handleMknod`, `handleReadlink`, `handleStatfs`, `handleUnlinkat`, `handleRenameat`, `handleRename`, `handleLock`, `handleGetlock`, `handleXattrwalk`, `handleXattrcreate` |
| `node.go` | 23 capability interfaces (`Node`, `NodeLookuper`, `NodeReader`, `NodeWriter`, ...) |
| `inode.go` | `Inode` struct: tree management, ENOSYS defaults for all capability interfaces |
| `fid.go` | `fidTable` (concurrent map), `fidState` (lifecycle tracking), state transitions |
| `flush.go` | `inflightMap`: tag tracking, per-request cancellation, drain-on-disconnect |
| `cleanup.go` | Orderly connection shutdown: cancel inflight, drain, clunk all fids, close channel |
| `middleware.go` | `Handler` type, `Middleware` type, `chain()` builder |
| `otel.go` | OTel tracing/metrics middleware, connection-level gauges |
| `logging.go` | `NewTraceHandler` (slog + trace ID correlation), `NewLoggingMiddleware` |
| `options.go` | Functional options: `WithMaxMsize`, `WithMaxInflight`, `WithLogger`, `WithAnames`, `WithAttacher`, `WithIdleTimeout` |
| `context.go` | `ConnInfo` struct, `ConnFromContext()` for per-connection metadata |
| `filehandle.go` | `FileHandle` marker interface, `FileReader`, `FileWriter`, `FileReleaser`, `FileReaddirer`, `FileRawReaddirer` |
| `composable.go` | `ReadOnlyFile`, `ReadOnlyDir` convenience types |
| `helpers.go` | `Symlink`, `Device`, `StaticFS` helper node types; `QIDGenerator`, `PathQID` |
| `dirent.go` | `EncodeDirents()` -- packs `[]Dirent` into wire-format bytes |
| `qid.go` | `QIDGenerator` (atomic counter), `PathQID` (FNV-1a deterministic), `nodeQID` resolution |
| `errors.go` | Sentinel errors: `ErrFidInUse`, `ErrFidNotFound`, `ErrNotNegotiated`, `ErrMsizeTooSmall` |

### `server/memfs/`

In-memory filesystem nodes for testing and synthetic file trees. Provides `MemFile` (read-write), `MemDir` (read-write directory with create/mkdir/unlink), and `StaticFile` (read-only). The `NewDir()` builder enables fluent tree construction:

```go
root := memfs.NewDir(gen).
    AddFile("config.json", data).
    AddStaticFile("version", "1.0.0").
    WithDir("data", func(d *memfs.MemDir) {
        d.AddFile("cache.db", nil)
    })
```

### `server/passthrough/`

Reference implementation that delegates all operations to the host filesystem via `*at` syscalls (`Openat`, `Fstatat`, `Mkdirat`, `Renameat`, `Unlinkat`, etc.). Nodes hold OS file descriptors. For non-directory files, `O_PATH` descriptors are opened on lookup and reopened via `/proc/self/fd/N` for actual I/O. Supports UID/GID mapping via the `UIDMapper` interface. Depends on `golang.org/x/sys/unix`.

### `server/fstest/`

Protocol-level conformance test harness. `Check(t, root)` or `CheckFactory(t, newRoot)` runs a suite of test cases (walk, read, write, readdir, create, mkdir, getattr, unlink, concurrent reads) against any `server.Node` implementation. Tests use `net.Pipe()` for in-memory connections with the actual 9P2000.L wire protocol. `NewTestTree()` constructs the expected tree shape.

## Key Abstractions

### Node and Capability Interfaces

`Node` is the minimal interface: a single method `QID() proto.QID`. The server discovers filesystem behavior through 23 optional capability interfaces defined in `server/node.go`:

| Interface | Method(s) | Purpose |
|-----------|-----------|---------|
| `NodeLookuper` | `Lookup(ctx, name) (Node, error)` | Directory child resolution (walk) |
| `NodeOpener` | `Open(ctx, flags) (FileHandle, uint32, error)` | Open a file, optionally return per-open state |
| `NodeReader` | `Read(ctx, offset, count) ([]byte, error)` | Read file data |
| `NodeWriter` | `Write(ctx, data, offset) (uint32, error)` | Write file data |
| `NodeGetattrer` | `Getattr(ctx, mask) (Attr, error)` | Retrieve file attributes |
| `NodeSetattrer` | `Setattr(ctx, SetAttr) error` | Modify file attributes |
| `NodeReaddirer` | `Readdir(ctx) ([]Dirent, error)` | Simple readdir (server manages offsets) |
| `NodeRawReaddirer` | `RawReaddir(ctx, offset, count) ([]byte, error)` | Raw readdir (node manages offsets) |
| `NodeCreater` | `Create(ctx, name, flags, mode, gid) (Node, FileHandle, uint32, error)` | Create + open in one step |
| `NodeMkdirer` | `Mkdir(ctx, name, mode, gid) (Node, error)` | Create subdirectory |
| `NodeSymlinker` | `Symlink(ctx, name, target, gid) (Node, error)` | Create symbolic link |
| `NodeLinker` | `Link(ctx, target, name) error` | Create hard link |
| `NodeMknoder` | `Mknod(ctx, name, mode, major, minor, gid) (Node, error)` | Create device node |
| `NodeReadlinker` | `Readlink(ctx) (string, error)` | Read symlink target |
| `NodeUnlinker` | `Unlink(ctx, name, flags) error` | Remove directory entry |
| `NodeRenamer` | `Rename(ctx, oldName, newDir, newName) error` | Move/rename entry |
| `NodeStatFSer` | `StatFS(ctx) (FSStat, error)` | Filesystem statistics |
| `NodeLocker` | `Lock(...)`, `GetLock(...)` | POSIX byte-range locking |
| `NodeCloser` | `Close(ctx) error` | Cleanup on clunk |
| `NodeXattrGetter` | `GetXattr(ctx, name) ([]byte, error)` | Read extended attribute |
| `NodeXattrSetter` | `SetXattr(ctx, name, data, flags) error` | Set extended attribute |
| `NodeXattrLister` | `ListXattrs(ctx) ([]string, error)` | List extended attribute names |
| `NodeXattrRemover` | `RemoveXattr(ctx, name) error` | Remove extended attribute |

Additionally, `RawXattrer` provides protocol-level control over the xattr two-phase flow (xattrwalk/xattrcreate), taking precedence over the simple xattr interfaces when implemented.

### Inode

`Inode` (`server/inode.go`) serves dual purposes:

1. **ENOSYS default provider** -- Implements all 23 capability interfaces with methods that return `proto.ENOSYS`. When users embed `*Inode` and override only the methods they need, unimplemented operations automatically fail with the correct error.

2. **Tree management** -- Maintains parent/child relationships via a `sync.Mutex`-protected `map[string]*Inode`. Provides `AddChild`, `RemoveChild`, `Children`, `Parent`, and a default `Lookup` that resolves children from this map.

Users embed `*Inode` in their node struct and call `Init(qid, self)` during construction. The bridge layer uses `InodeEmbedder` to access the embedded Inode for tree operations.

### FileHandle

`FileHandle` is a marker interface for per-open state returned by `NodeOpener.Open()`. When a `FileHandle` implements `FileReader`, `FileWriter`, `FileReaddirer`, or `FileRawReaddirer`, those methods take priority over the corresponding Node-level methods for that open instance. This enables stateful I/O (e.g., the passthrough filesystem uses `fileHandle` to wrap an OS file descriptor opened with specific flags). `FileReleaser` is called on clunk to clean up handle resources.

### Middleware

The `Handler` type (`func(ctx, tag, msg) Message`) is the unit of dispatch. `Middleware` wraps a `Handler` to add cross-cutting behavior. The `chain()` function composes middleware: the first middleware added is outermost (first to execute). When OTel providers are configured, tracing and metrics middleware is automatically prepended. `NewLoggingMiddleware` is provided as a built-in middleware.

## Request Lifecycle

A 9P request follows this path from network bytes to filesystem operation:

1. **Accept** -- `Server.Serve()` accepts a `net.Conn` and spawns a goroutine running `conn.serve()`.

2. **Version negotiation** -- `negotiateVersion()` reads the first `Tversion`, negotiates msize (min of client and server max, with floor at 256 bytes), selects the protocol dialect (`9P2000.L` or `9P2000.u`), assigns the matching codec, and sends `Rversion`. The connection is closed if the version is unknown.

3. **Read loop** -- `readLoop()` reads framed messages from the wire: 4-byte size prefix, then the remaining bytes. The message type byte determines routing:
   - `Tversion` mid-connection triggers `handleReVersion` (drains inflight, clunks all fids, re-negotiates).
   - `Tflush` is handled synchronously in the read loop to avoid deadlock when all semaphore slots are taken.
   - All other messages are decoded via `newMessage()` (protocol-specific factory), then dispatched concurrently.

4. **Semaphore** -- Before spawning a request goroutine, the read loop acquires a slot from a buffered channel of size `maxInflight` (default 64). This bounds concurrent request handlers per connection.

5. **Per-request goroutine** -- `handleRequest()` runs with panic recovery. It calls the middleware-wrapped handler chain, sends the response via `sendResponse()`, releases the semaphore slot, and clears the inflight entry.

6. **Middleware chain** -- The handler chain (built once in `newConn`) wraps `dispatch()` with any configured middleware. When OTel is configured, the outermost middleware creates a span, records request/response sizes, and tracks active requests.

7. **Dispatch** -- `dispatch()` type-switches on the decoded message to route to the appropriate handler (`handleAttach`, `handleWalk`, `handleClunk`, or a bridge handler).

8. **Bridge** -- Bridge handlers (in `bridge.go`) translate protocol messages into capability interface calls:
   - Look up the fid in the `fidTable` to get the `fidState` (node + lifecycle status).
   - Check the fid's state (allocated, opened, xattr mode).
   - Type-assert the node to the required capability interface.
   - Call the interface method with the request parameters.
   - Construct and return the response message.
   - For operations that return new nodes (create, mkdir, symlink, mknod), register the child in the Inode tree.

   The bridge uses a two-level dispatch for read/write/readdir: `FileHandle` methods take priority over `Node` methods when a handle is present, allowing per-open state.

9. **Write loop** -- A single writer goroutine (`writeLoop`) drains the `responses` channel and encodes each response to the `net.Conn` using the negotiated codec. The `writeMu` mutex serializes writes between the write loop and `writeRaw` (used during version negotiation).

10. **Response** -- `sendResponse()` queues a `taggedResponse` on the responses channel. If the channel is closed (cleanup completed), a deferred recover drops the response silently.

## Concurrency Model

The server uses three levels of goroutine concurrency:

### Goroutine-per-Connection

`Server.Serve()` spawns one goroutine per accepted connection. The goroutine owns the connection lifecycle from version negotiation through cleanup. A `sync.WaitGroup` in `Serve()` tracks all connection goroutines.

### Goroutine-per-Request

Within a connection, each incoming request (except `Tflush` and `Tversion`) spawns a handler goroutine. Concurrency is bounded by the `semaphore` channel (buffered to `maxInflight`, default 64). The `inflightMap` tracks every active request by tag, storing a `context.CancelFunc` for flush support.

`Tflush` is handled synchronously in the read loop to prevent deadlock: if all semaphore slots are occupied, a flush must still be able to cancel a pending request and free a slot.

### Single Writer

All response encoding flows through a single writer goroutine (`writeLoop`) that drains the `responses` channel. This eliminates the need for per-write locking on the hot path. The `writeMu` mutex exists only for `writeRaw`, used during version negotiation (before the write loop starts or during mid-connection re-negotiation).

### Key Synchronization Primitives

| Primitive | Location | Purpose |
|-----------|----------|---------|
| `sync.RWMutex` | `fidTable` | Concurrent fid lookup (RLock) vs add/clunk/update (Lock) |
| `sync.Mutex` | `fidState.mu` | Protects xattr buffer and dir cache within a fid |
| `sync.Mutex` | `Inode.mu` | Protects parent/child tree relationships |
| `sync.Mutex` | `inflightMap.mu` | Protects tag-to-cancel map |
| `sync.WaitGroup` | `inflightMap.wg` | Drain-on-disconnect: waits for all handlers to finish |
| `chan struct{}` | `conn.semaphore` | Counting semaphore bounding concurrent requests |
| `chan taggedResponse` | `conn.responses` | Single-writer response channel |
| `sync.Mutex` | `conn.writeMu` | Serializes writeRaw with writeLoop |
| `context.CancelFunc` | per-request | Flush cancellation via `inflightMap.flush(tag)` |

## Fid State Machine

Each fid tracked in the `fidTable` has a lifecycle state (`fidStatus`):

```
                 Tattach / Twalk
                       │
                       ▼
             ┌─── fidAllocated ───┐
             │                    │
   Tlopen    │                    │  Txattrcreate
             ▼                    ▼
        fidOpened           fidXattrWrite
             │                    │
   Tclunk    │         Tclunk     │  (commits xattr)
             ▼                    ▼
          (removed)           (removed)

  Txattrwalk creates a NEW fid in fidXattrRead state.
  Tclunk always removes the fid from the table.
```

**States:**

| State | Entered via | Allowed operations |
|-------|-------------|-------------------|
| `fidAllocated` | `Tattach`, `Twalk` (new fid) | `Tlopen`, `Tgetattr`, `Tsetattr`, `Twalk` (re-walk), `Tclunk`, `Txattrwalk`, `Txattrcreate` |
| `fidOpened` | `Tlopen`, `Tlcreate` | `Tread`, `Twrite`, `Treaddir`, `Tgetattr`, `Tsetattr`, `Tlock`, `Tgetlock`, `Tclunk` |
| `fidXattrRead` | `Txattrwalk` (new fid) | `Tread` (from cached buffer), `Tclunk` |
| `fidXattrWrite` | `Txattrcreate` (mutates existing fid) | `Twrite` (accumulates data), `Tclunk` (commits xattr) |

The `fidState` struct holds the node, walked path, open handle, directory cache, and xattr buffers. State transitions are protected by `fidTable`'s `sync.RWMutex` and individual `fidState.mu` locks.

On clunk, the server releases the `FileHandle` (via `FileReleaser`) and calls `NodeCloser.Close()` if implemented. For xattr fids, clunk commits the accumulated xattr data.

## Connection Cleanup

When a connection ends (client disconnect, context cancellation, or read error), `cleanup()` runs a four-step orderly shutdown:

1. **Cancel all inflight** -- `inflightMap.cancelAll()` cancels every active request's context.
2. **Drain with deadline** -- `inflightMap.waitWithDeadline()` waits up to 5 seconds (`cleanupDeadline`) for handler goroutines to finish.
3. **Clunk all fids** -- `fidTable.clunkAll()` atomically swaps the fid map, then iterates outside the lock to release handles and call `NodeCloser.Close()`.
4. **Close responses channel** -- Terminates the writer goroutine.

Mid-connection `Tversion` re-negotiation follows the same pattern (cancel, drain, clunk all fids) before re-negotiating the protocol.

## Extension Points

### Custom Attach Logic

- **`WithAnames(map[string]Node)`** -- Maps attach names to root nodes for vhost-style dispatch. The client's `Tattach.Aname` field selects which tree to serve.
- **`WithAttacher(Attacher)`** -- Full-control attach handler. The `Attacher` interface has a single method `Attach(ctx, uname, aname) (Node, error)` that receives the client's credentials and returns the root node. Takes precedence over both the default root and aname dispatch.

### Middleware

User-defined middleware wraps the dispatch `Handler` to add behavior like access control, rate limiting, or custom logging. Middleware is added via `WithMiddleware()` and composes in order (first added is outermost).

### Observability

- **`WithTracer(TracerProvider)`** -- Enables per-operation OTel spans with attributes: `rpc.system.name`, `rpc.method`, `ninep.fid`, `ninep.path`, `ninep.protocol`.
- **`WithMeter(MeterProvider)`** -- Enables OTel metrics: `ninep.server.duration` (histogram), `ninep.server.request.size` / `ninep.server.response.size` (counters), `ninep.server.active_requests` (gauge), `ninep.server.connections` (gauge), `ninep.server.fid.count` (gauge).
- **`NewTraceHandler(slog.Handler)`** -- Wraps any slog handler with automatic `trace_id` and `span_id` attribute injection when an active OTel span is present.
- **`NewLoggingMiddleware(logger)`** -- Logs each 9P request at Debug level with operation type, duration, and error status.

### ConnInfo

`ConnFromContext(ctx)` returns `*ConnInfo` containing the negotiated protocol version, msize, and remote address. Available in all node handler methods.

### QID Generation

- **`QIDGenerator`** -- Thread-safe monotonic counter for generating unique QID paths. `Next(type)` returns a new QID.
- **`PathQID(type, path)`** -- Deterministic QID from a path string using FNV-1a hashing.

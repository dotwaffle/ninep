<!-- generated-by: gsd-doc-writer -->
# Architecture

## System Overview

ninep is a Go library implementing the 9P2000.L and 9P2000.u network filesystem protocols. It accepts raw TCP or Unix socket connections, negotiates a protocol dialect, then dispatches incoming 9P messages to user-defined filesystem nodes through a capability-based interface inspired by go-fuse/v2/fs. The primary architectural style is layered: a wire encoding layer (`proto/`), protocol-specific codecs (`proto/p9l/`, `proto/p9u/`), and a server core (`server/`) that bridges protocol messages to filesystem operations. The library depends on the OpenTelemetry API for observability and the Go standard library for everything else.

## Component Diagram

```
                net.Listener
                     │
               ┌─────▼─────┐
               │  Server   │  Accepts connections, holds config
               └─────┬─────┘
                     │  goroutine-per-connection
               ┌─────▼─────┐
               │   conn    │  Version negotiation, recv-mutex worker
               └─────┬─────┘  model, fid table, inflight map
                     │
               ┌─────▼─────────┐
               │ handleRequest │  Lock recvMu, read+decode one frame,
               │   (loop)      │  spawn successor (if none parked AND
               └─────┬─────────┘  workerCount < maxInflight), unlock
                     │            recvMu, dispatch INLINE, write reply,
                     │            loop. Bounded by maxInflight.
                     │
            ┌────────▼────────┐
            │ dispatchInline  │  panic recovery, bufPtr release,
            │  middleware →   │  cached-msg release, finish(tag)
            │  dispatch →     │
            │  bridge         │
            └────────┬────────┘
                     │
            ┌────────▼────────┐
            │ sendResponseInline │  encode body via bufpool;
            │                    │  writev {hdr, body [, payload]}
            │                    │  under writeMu
            └────────┬───────────┘
                     │
              ┌──────▼──────┐
              │ Node (user) │  Capability interfaces
              │ embed Inode │  (NodeReader, NodeWriter, ...)
              └─────────────┘

Wire encoding:
  proto/      shared types, Message interface, Payloader, encode/decode helpers
  proto/p9l/  9P2000.L codec (Encode/Decode)
  proto/p9u/  9P2000.u codec (Encode/Decode)

Allocation paths:
  internal/bufpool/   size-classed []byte buckets (1K/4K/64K/1M) for
                      message bodies and encode buffers; separate
                      *bytes.Buffer pool for Encode destinations.
  server/msgcache.go  per-type bounded channel caches (cap=3) for
                      Tread/Twrite/Twalk/Tclunk/Tlopen/Tgetattr structs.
```

## Package Responsibilities

```
ninep/
├── proto/                 Wire types, Message interface, Payloader, encoding helpers
│   ├── p9l/               9P2000.L codec and message structs
│   └── p9u/               9P2000.u codec and message structs
├── internal/
│   └── bufpool/           Size-classed []byte and *bytes.Buffer pools
└── server/                Server core, dispatch, capability bridge
    ├── memfs/             In-memory node types with fluent builder API
    ├── passthrough/       Host-filesystem passthrough using *at syscalls
    └── fstest/            Protocol-level conformance test harness
```

### `proto/`

Defines the shared vocabulary for all 9P communication:

- **`Message` interface** (`message.go`) -- `Type()`, `EncodeTo(io.Writer)`, `DecodeFrom(io.Reader)`. Every T-message and R-message implements this. The interface handles the body only; the 7-byte header (size[4] + type[1] + tag[2]) is managed by the codec layer.
- **`Payloader` interface** (`messages.go`) -- Optional secondary interface implemented by `Rread` and `Rreaddir`. Provides `EncodeFixed(io.Writer)` (non-payload prefix: the 4-byte count) and `Payload() []byte` (the opaque data). The server uses this to assemble a three-entry `net.Buffers` and emit `{hdr, fixedBody, payload}` in a single `writev`, avoiding a memcpy of the payload into the body buffer.
- **Wire types** (`types.go`) -- `QID`, `Fid`, `Tag`, `Attr`, `AttrMask`, `SetAttr`, `Dirent`, `FSStat`, `FileMode`, lock types.
- **Encoding helpers** (`encode.go`, `decode.go`) -- `WriteUint32`, `ReadUint32`, `WriteString`, `ReadString`, etc. These use `encoding/binary.LittleEndian` directly for zero-allocation hot paths.
- **Shared messages** (`messages.go`) -- `Tversion`/`Rversion`, `Tattach`/`Rattach`, `Twalk`/`Rwalk`, `Tread`/`Rread`, `Twrite`/`Rwrite`, `Tclunk`/`Rclunk`, `Tflush`/`Rflush`, and other base protocol messages used by both dialects. `Twrite.DecodeFromBuf([]byte)` aliases `m.Data` into the caller-supplied buffer (zero-copy write path).
- **Errno** (`errno.go`) -- `Errno` type implementing `error`, with constants for standard POSIX error numbers (`ENOENT`, `EIO`, `ENOSYS`, etc.).
- **Constants** (`constants.go`) -- `HeaderSize` (7), `MaxWalkElements` (16), `QIDSize` (13), `NoTag`, `NoFid`.

### `proto/p9l/`

9P2000.L codec for Linux kernel v9fs. Provides top-level `Encode(io.Writer, Tag, Message)` and `Decode(io.Reader)` functions. Defines all 9P2000.L-specific message structs: `Tlopen`/`Rlopen`, `Tgetattr`/`Rgetattr`, `Treaddir`/`Rreaddir`, `Tlock`/`Rlock`, `Txattrwalk`/`Rxattrwalk`, and others. `Rreaddir` also implements `proto.Payloader`.

### `proto/p9u/`

9P2000.u codec for Unix extensions. Same `Encode`/`Decode` API shape as p9l. Defines `Topen`/`Ropen`, `Tstat`/`Rstat`, `Rerror` (string-based errors with errno), and other 9P2000.u-specific messages.

### `internal/bufpool/`

Process-wide buffer pools shared across `proto`, `proto/p9l`, `proto/p9u`, and `server`. Living under `internal/` enforces the "only ninep may import" property required by the design doc.

- **`GetBuf` / `PutBuf`** -- `sync.Pool` of `*bytes.Buffer` pre-grown to 1 MiB. Used as the encode destination in `sendResponseInline` and `writeRaw`. A cap-guard (`PoolMaxBufSize`) drops oversized buffers to GC rather than retaining them.
- **`GetMsgBuf(n)` / `PutMsgBuf`** -- Size-classed `*[]byte` buckets: 1 KiB, 4 KiB, 64 KiB, 1 MiB. `handleRequest` borrows from the smallest bucket that fits the frame body; `Twrite.DecodeFromBuf` aliases into the borrowed buffer for zero-copy. Bucketing eliminates the pool-drain feedback loop where a 7-byte Tclunk would claim a 1 MiB buffer and amplify `seq_read_4k` throughput variance. `*[]byte` (not `[]byte`) is pooled to avoid the `any` boxing alloc.
- **`GetStringBuf` / `PutStringBuf`** -- Dedicated small-scratch pool for `proto.ReadString`, sized for names/paths/version strings.

### `server/`

The server core. Key files and their roles:

| File | Responsibility |
|------|---------------|
| `server.go` | `Server` struct, `New()` constructor, `Serve()` accept loop, `ServeConn()` |
| `conn.go` | `conn` struct, version negotiation, `handleRequest` recv-mutex loop, `dispatchInline`, `sendResponseInline`, `writeRaw`, `codec` abstraction |
| `cleanup.go` | Orderly connection shutdown: cancel inflight, drain, close `nc`, wait for `recvWG`, clunk all fids |
| `dispatch.go` | `dispatch()` type-switch routing, `handleAttach`, `handleWalk`, `handleClunk`, `handleFlush` |
| `bridge.go` | Capability bridge handlers; `pooledRread`/`pooledRreaddir` wrappers that carry a `bufpool.PutMsgBuf` callback via the `releaser` interface |
| `msgcache.go` | Bounded per-type struct caches (cap 3) for `Tread`, `Twrite`, `Twalk`, `Tclunk`, `Tlopen`, `Tgetattr`; `putCachedMsg` releases on request completion |
| `node.go` | 23 capability interfaces (`Node`, `NodeLookuper`, `NodeReader`, `NodeWriter`, ...) |
| `inode.go` | `Inode` struct: tree management, ENOSYS defaults for all capability interfaces |
| `fid.go` | `fidTable` (concurrent map), `fidState` (lifecycle tracking), state transitions |
| `flush.go` | `inflightMap`: tag tracking, per-request cancellation, drain-on-disconnect |
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
| `NodeReader` | `Read(ctx, buf, offset) (int, error)` | Read file data into caller buffer |
| `NodeWriter` | `Write(ctx, data, offset) (uint32, error)` | Write file data |
| `NodeGetattrer` | `Getattr(ctx, mask) (Attr, error)` | Retrieve file attributes |
| `NodeSetattrer` | `Setattr(ctx, SetAttr) error` | Modify file attributes |
| `NodeReaddirer` | `Readdir(ctx) ([]Dirent, error)` | Simple readdir (server manages offsets) |
| `NodeRawReaddirer` | `RawReaddir(ctx, buf, offset) (int, error)` | Raw readdir into caller buffer (node manages offsets) |
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

### Payloader and `releaser`

Response messages that carry a large opaque payload (`*proto.Rread`, `*p9l.Rreaddir`) implement `proto.Payloader` so `sendResponseInline` can emit the payload as a separate `net.Buffers` entry, bypassing the memcpy into the body buffer. Bridge handlers that populate these payloads from the size-classed bufpool wrap the response in `pooledRread` / `pooledRreaddir`, which implement the package-private `releaser` interface (single method: `Release()`). `sendResponseInline` calls `Release` after the `writev` completes so the pooled buffer returns to `bufpool.PutMsgBuf` even when the write fails.

## Request Lifecycle

A 9P request follows this path from network bytes to filesystem operation:

1. **Accept** -- `Server.Serve()` accepts a `net.Conn` and spawns a goroutine running `conn.serve()`.

2. **Version negotiation** -- `negotiateVersion()` reads the first `Tversion`, negotiates msize (min of client `Tversion.Msize` and server `maxMsize`, with floor at 256 bytes — the server default `maxMsize` is 1 MiB), selects the protocol dialect (`9P2000.L` or `9P2000.u`), assigns the matching codec, and sends `Rversion` via `writeRaw` (holds `writeMu`). The connection is closed if the version is unknown.

3. **Recv-mutex receive loop** -- `handleRequest()` is the single goroutine type that drives reception AND dispatch. It locks `recvMu`, reads one framed message from the wire (4-byte size prefix on a stack-local `hdrBuf`, then body into a buffer borrowed from `bufpool.GetMsgBuf`), decodes the body INSIDE `recvMu` so per-iteration scratch (e.g. `bytes.Reader`) stays safely owned by the lock holder, decides whether to spawn a successor, releases `recvMu`, then dispatches the request and writes the reply inline. The same goroutine that read the bytes is the one that handles the request and writes the response — there is no inter-goroutine handoff between read and dispatch.

   Routing inside the loop:
   - `Tversion` mid-connection triggers `handleReVersion` (drains inflight, clunks all fids, re-negotiates via `writeRaw`). Spawn-replacement is skipped on `Tversion` so the renegotiating goroutine is the sole reader during the codec/msize swap.
   - `Tflush` short-circuits to `handleFlush` AFTER `recvMu` is released (it operates on OTHER tags' inflight state and must not itself create an inflight entry).
   - All other messages are decoded via `newMessage()`. Hot types (`Tread`, `Twrite`, `Twalk`, `Tclunk`, `Tlopen`, `Tgetattr`) are pulled from the `msgcache` bounded channel; cache miss allocates fresh.

4. **Zero-copy Twrite** -- `Twrite.DecodeFromBuf` aliases `m.Data` directly into the pooled frame buffer so write payloads never incur a memcpy between read and handler. The message body buffer pointer is carried as a local in `handleRequest` and released by `dispatchInline`'s defer after the handler returns. All other decodes use a per-iteration `bytes.Reader` to avoid a per-message alloc; those message types copy fields out during `DecodeFrom`, so the frame buffer is returned to `bufpool` immediately.

5. **Spawn-replacement decision** -- After reading a request, the dispatcher decides whether to spawn a successor before releasing `recvMu`: only if no sibling is parked on `recvMu` (`recvIdle == 0`) AND `workerCount < maxInflight`. The successor takes over reading the next request while the current dispatcher handles the one it just read. The `maxInflight` cap (default 64) bounds total goroutine count per connection, providing back-pressure: if all workers are dispatching, the next request sits in the kernel socket buffer until one returns to the loop.

6. **dispatchInline** -- Called by `handleRequest` after `recvMu` is released. Runs the request through the middleware + dispatch chain with panic recovery. On exit (deferred): release the zero-copy `bufPtr` if present, return the request struct to its `msgcache` via `putCachedMsg`, emit an `EIO` response if the handler panicked, and call `c.inflight.finish(tag)`. The handler itself is `c.handler` — the middleware-wrapped dispatch chain built once in `newConn`.

7. **Middleware chain** -- When OTel is configured, the outermost middleware creates a span, records request/response sizes, and tracks active requests. Zero middleware incurs zero overhead (`chain` returns the inner handler directly).

8. **Dispatch** -- `dispatch()` type-switches on the decoded message to route to the appropriate handler (`handleAttach`, `handleWalk`, `handleClunk`, or a bridge handler).

9. **Bridge** -- Bridge handlers (in `bridge.go`) translate protocol messages into capability interface calls:
    - Look up the fid in the `fidTable` to get the `fidState` (node + lifecycle status).
    - Check the fid's state (allocated, opened, xattr mode).
    - Type-assert the node to the required capability interface.
    - Call the interface method with the request parameters.
    - Construct and return the response message.
    - For operations that return new nodes (create, mkdir, symlink, mknod), register the child in the Inode tree.

    The bridge uses a two-level dispatch for read/write/readdir: `FileHandle` methods take priority over `Node` methods when a handle is present, allowing per-open state. For Rread/Rreaddir, the bridge borrows a sized buffer via `bufpool.GetMsgBuf`, asks the capability to fill it, and returns a `pooledRread`/`pooledRreaddir` wrapper so the buffer is released after the writev.

10. **Inline response** -- `dispatchInline` calls `sendResponseInline(tag, resp, rel)`. The function encodes the body into a `*bytes.Buffer` borrowed from `bufpool` (outside `writeMu` to keep the critical section short). For `Payloader` messages it calls `EncodeFixed` and captures `Payload()` separately. It then acquires `writeMu`, fills the conn-resident `encHdr` (size + type + tag), assembles `net.Buffers{hdr, body}` or `{hdr, body, payload}` in the conn-resident `encBufsArr`, calls `bufs.WriteTo(c.nc)` (a single `writev` syscall on TCP and Unix sockets), then releases the mutex, returns the body buffer to bufpool, and invokes the releaser.

## Concurrency Model

The server uses two levels of goroutine concurrency. The recv-mutex worker model collapses what was previously a read-loop -> worker handoff into a single goroutine type per request (saving ~1-3 µs of inter-goroutine context switch on real unix-socket transports), and caps goroutine churn at the per-connection `maxInflight` bound.

### Goroutine-per-Connection

`Server.Serve()` spawns one goroutine per accepted connection. The goroutine owns the connection lifecycle from version negotiation through cleanup. A `sync.WaitGroup` in `Serve()` tracks all connection goroutines.

### Recv-Mutex Worker Model (lazy-spawn under recvMu, bounded)

Within a connection, requests are processed by a pool of `handleRequest` goroutines sized dynamically up to `maxInflight` (default 64). A goroutine spawns a successor only when no sibling is parked on `recvMu` (`recvIdle == 0`) AND `workerCount < maxInflight`. Successors are long-lived for the connection lifetime and re-acquire `recvMu` after each dispatch. Back-pressure is provided by the cap on spawned workers — when every dispatcher is busy in handler code and `workerCount` is at the cap, no replacement reader is spawned, and the next request sits in the kernel socket buffer until one of the dispatchers finishes and loops back to read.

`Tflush` is handled inline by the goroutine that read it (after `recvMu` is released). It calls `handleFlush` directly, bypassing `inflight.start`/`dispatchInline` because flush operates on OTHER tags' inflight state and must not itself create an inflight entry.

### Inline Writes

There is no writer goroutine. Each `handleRequest` goroutine calls `sendResponseInline` directly to encode and emit its response. Concurrent dispatchers on the same connection serialise at `writeMu`, which covers both the shared `encHdr`/`encBufsArr` backing store and the `net.Conn.Write`. `writeRaw` (used during initial and mid-connection version negotiation) acquires the same mutex, so version negotiation and inline writes cannot interleave wire frames.

### Key Synchronization Primitives

| Primitive | Location | Purpose |
|-----------|----------|---------|
| `sync.RWMutex` | `fidTable` | Concurrent fid lookup (RLock) vs add/clunk/update (Lock) |
| `sync.Mutex` | `fidState.mu` | Protects xattr buffer and dir cache within a fid |
| `sync.Mutex` | `Inode.mu` | Protects parent/child tree relationships |
| `sync.Mutex` | `inflightMap.mu` | Protects tag-to-cancel map |
| `sync.WaitGroup` | `inflightMap.wg` | Drain-on-disconnect: waits for all handlers to finish |
| `sync.Mutex` | `conn.recvMu` | Serialises receiving from `nc`; held only across read+decode |
| `atomic.Int32` | `conn.recvIdle` | Count of goroutines parked in `recvMu.Lock()`; spawn-replacement predicate |
| `bool` | `conn.recvShutdown` (under `recvMu`) | First goroutine to observe a recv error sets this; siblings exit on next acquire |
| `chan struct{}` | `conn.recvShutdownCh` | One-shot signal: `serve()` waits on this to begin cleanup |
| `atomic.Int32` | `conn.workerCount` | Enforces `WithMaxInflight` cap on `handleRequest` goroutine count |
| `sync.WaitGroup` | `conn.recvWG` | Cleanup drain: waits for `handleRequest` goroutines to exit |
| `sync.Mutex` | `conn.writeMu` | Serialises `sendResponseInline` writes and `writeRaw` |
| `context.CancelFunc` | per-request | Flush cancellation via `inflightMap.flush(tag)` |
| bounded `chan *T` | `server/msgcache.go` | Per-type struct cache (cap 3) for Tread/Twrite/Twalk/Tclunk/Tlopen/Tgetattr |
| `sync.Pool` | `internal/bufpool/` | `*bytes.Buffer` encode pool and size-classed `*[]byte` buckets |

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

When a connection ends (client disconnect, context cancellation, or read error), the first `handleRequest` goroutine to observe the recv error closes `recvShutdownCh`. `serve()` waits on this signal (NOT on `recvWG`, which would deadlock if handlers are blocked in dispatch waiting for cancelAll), then runs `cleanup()`:

1. **Cancel all inflight** -- `inflightMap.cancelAll()` cancels every active request's context so handlers respecting `ctx.Done()` return promptly.
2. **Drain with deadline** -- `inflightMap.waitWithDeadline()` waits up to 5 seconds (`cleanupDeadline`) for handler goroutines to finish.
3. **Close `nc` and wait for `recvWG`** -- `c.nc.Close()` (idempotent) unblocks the recvMu holder out of any in-progress read. Goroutines parked on `recvMu.Lock()` observe `recvShutdown` on acquire and exit. Busy dispatchers exit after their handler returns and the loop re-acquires `recvMu`. `recvWG.Wait` is bounded by the same `cleanupDeadline`; stuck handlers that ignore context cancellation are logged and orphaned rather than hung on.
4. **Clunk all fids** -- `fidTable.clunkAll()` atomically swaps the fid map, then iterates outside the lock to release handles and call `NodeCloser.Close()`.

Because dispatchers write responses inline, there is no response channel to drain. Mid-connection `Tversion` re-negotiation (`handleReVersion`) follows the same cancel + drain + clunk-all-fids pattern before re-negotiating the protocol and sending `Rversion` via `writeRaw`.

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

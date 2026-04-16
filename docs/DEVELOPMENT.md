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

The `proto/p9l` and `proto/p9u` packages include fuzz tests. CI runs `FuzzCodecRoundTrip` for 30 seconds per codec; run locally with:

```bash
go test -fuzz=FuzzCodecRoundTrip -fuzztime=30s ./proto/p9l/
go test -fuzz=FuzzCodecRoundTrip -fuzztime=30s ./proto/p9u/
```

## Project Structure

```
ninep/
  go.mod
  internal/
    bufpool/            Process-wide []byte and *bytes.Buffer pools (internal-only)
      bufpool.go          GetMsgBuf/PutMsgBuf (bucketed 1K/4K/64K/1M), GetBuf/PutBuf, GetStringBuf/PutStringBuf
  proto/              Wire types, constants, encoding helpers
    constants.go        HeaderSize, NoFid, NoTag, QIDSize
    types.go            QID, Attr, Dirent, FSStat, Fid, Tag, etc.
    message.go          Message interface, MessageType enum, Payloader interface
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
    conn.go             Per-connection lifecycle: read loop, worker pool (lazy-spawn), version negotiation, sendResponseInline
    dispatch.go         Message routing to bridge handlers
    bridge.go           Bridge handlers (handleLopen, handleRead, handleWrite, etc.); pooledRread/pooledRreaddir wrappers
    fid.go              Fid table with lifecycle state tracking
    flush.go            Inflight request tracking and Tflush cancellation
    cleanup.go          Connection shutdown: cancel inflight, drain, close workCh, clunk fids
    msgcache.go         Bounded chan caches for hot request types (Tread/Twrite/Twalk/Tclunk/Tlopen/Tgetattr)
    middleware.go       Handler/Middleware types, chain(), WithMiddleware()
    options.go          Functional options (WithMaxMsize, WithMaxInflight, WithLogger, etc.)
    errors.go           Sentinel errors (ErrFidInUse, ErrNotNegotiated, etc.)
    filehandle.go       FileHandle, FileReader, FileWriter, FileReleaser interfaces
    composable.go       ReadOnlyFile, ReadOnlyDir, Symlink, Device, StaticFS helpers
    helpers.go          Symlink, Device, StaticFS constructors (SymlinkTo, DeviceNode, etc.)
    qid.go              QIDGenerator and PathQID helper
    context.go          ConnInfo and ConnFromContext
    dirent.go           EncodeDirents helper
    otel.go             OpenTelemetry middleware and connection-level instruments
    logging.go          NewTraceHandler (slog + OTel correlation), NewLoggingMiddleware
    doc.go              Package-level godoc
    bench_test.go       Round-trip + contention benchmarks; connPair test helper
    io_bench_test.go    Read/write/readdir benchmarks; newConnPairMsize, benchWalkOpen, treadOffsetPos
    writev_bench_test.go  net.Buffers / writev A/B harness; unixPair, pipePair
    msgalloc_bench_test.go  Per-request message-struct alloc comparison
    readloop_alloc_test.go  Alloc assertions for the readLoop/decode path
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
    Fsync(ctx context.Context) error
}
```

Interface naming convention: `Node` + operation name + `er` suffix. Examples from the codebase (current signatures):

| Interface | Method | 9P Operation |
|-----------|--------|--------------|
| `NodeReader` | `Read(ctx, buf []byte, offset uint64) (int, error)` | Tread |
| `NodeWriter` | `Write(ctx, data []byte, offset uint64) (uint32, error)` | Twrite |
| `NodeOpener` | `Open(ctx, flags uint32) (FileHandle, uint32, error)` | Tlopen |
| `NodeGetattrer` | `Getattr(ctx, mask proto.AttrMask) (proto.Attr, error)` | Tgetattr |
| `NodeReaddirer` | `Readdir(ctx) ([]proto.Dirent, error)` | Treaddir (library packs) |
| `NodeRawReaddirer` | `RawReaddir(ctx, buf []byte, offset uint64) (int, error)` | Treaddir (self-packed) |
| `NodeCreater` | `Create(ctx, name, flags, mode, gid)` | Tlcreate |
| `NodeMkdirer` | `Mkdir(ctx, name, mode, gid)` | Tmkdir |
| `NodeLookuper` | `Lookup(ctx, name)` | Twalk (per element) |
| `NodeUnlinker` | `Unlink(ctx, name, flags)` | Tunlinkat |
| `NodeRenamer` | `Rename(ctx, oldName, newDir, newName)` | Trenameat |
| `NodeStatFSer` | `StatFS(ctx)` | Tstatfs |
| `NodeFsyncer` | `Fsync(ctx) error` | Tfsync |
| `NodeLocker` | `Lock(...)` / `GetLock(...)` | Tlock / Tgetlock |

**Read and RawReaddir are buf-passing.** Since v1.1.3 the server pulls a pooled body buffer from `internal/bufpool`, clamps it to the Tread/Treaddir count (capped at msize), and hands it to your `Read` / `RawReaddir`. Fill the buffer in place and return the count written. Do **not** retain the slice past the call -- `pooledRread`/`pooledRreaddir` in `bridge.go` wrap the response and release the buffer back to the pool after the writev completes.

```go
func (f *myFile) Read(ctx context.Context, buf []byte, offset uint64) (int, error) {
    // Fill buf in place; do not append, do not retain buf after return.
    return copy(buf, f.data[offset:]), nil
}
```

### Inode Embedding

Nodes embed `Inode` (via `server.Inode` struct embedding) and call `Init` during construction:

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
- `Init(qid, self)` sets the QID and the back-reference so the Inode tree resolves to your struct (not the embedded `Inode`)
- `Inode` implements all capability interfaces with ENOSYS returns -- compile-time assertions in `inode.go` enforce this
- Override by implementing the capability interface on your struct; the bridge uses type assertions to detect your implementation at runtime

### Functional Options

The server uses the `Option` pattern defined in `server/options.go`:

```go
srv := server.New(root,
    server.WithMaxMsize(1 << 20),        // 1MB max message size
    server.WithMaxInflight(128),         // 128 concurrent requests (== worker pool cap)
    server.WithMaxFids(100_000),         // per-conn fid cap
    server.WithLogger(slog.Default()),   // structured logger
    server.WithIdleTimeout(30*time.Second),
    server.WithTracer(tp),               // OTel TracerProvider
    server.WithMeter(mp),                // OTel MeterProvider
    server.WithMiddleware(myMiddleware), // custom middleware
)
```

Defaults (from `server.go` `New` function):
- `maxMsize`: 1048576 (1 MiB)
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

func (h *myHandle) Read(ctx context.Context, buf []byte, offset uint64) (int, error) {
    // per-open state: fill buf, return bytes read
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
    Fsync(ctx context.Context) error
}
```

Follow existing naming: `Node` + verb + `er`. First parameter is always `context.Context`.

### Step 2: Add the ENOSYS Default to Inode

In `server/inode.go`, add a default method that returns `proto.ENOSYS`:

```go
// Fsync returns proto.ENOSYS. Override by implementing NodeFsyncer.
func (i *Inode) Fsync(_ context.Context) error {
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

    if err := fsyncer.Fsync(ctx); err != nil {
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

If the response carries a pooled buffer (see `pooledRread` / `pooledRreaddir` in `bridge.go`), the wrapper's `Release` method is invoked by `sendResponseInline` after the writev completes.

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

## Connection Model (Worker Pool)

Understanding the current connection model matters when extending the server or profiling performance.

- **Goroutine-per-connection** -- each accepted `net.Conn` runs `conn.serve`, which spins a read loop and a lazy-spawn worker pool.
- **Lazy-spawn worker pool** -- workers are bounded by `maxInflight` and only grow on demand. The read loop hands decoded requests to `c.workCh` (buffered at `maxInflight`); an idle worker picks them up, runs them through the middleware chain + `dispatch`, and writes the response inline. Workers persist for the lifetime of the connection.
- **Inline writes (no writeLoop goroutine)** -- since v1.1.15 the worker that ran the handler also encodes and sends the response from `sendResponseInline`, which acquires `writeMu`, issues a single `net.Buffers.WriteTo` (one writev on sockets that support it), and then calls `Release` on any pooled-buffer responses. There is no `responses` channel and no dedicated writeLoop goroutine.
- **Per-request panic recovery** -- `handleWorkItem` wraps the handler in `defer recover()`. A panicking handler becomes an `Rlerror{Ecode: EIO}` on that tag; the worker survives and keeps draining `workCh`.
- **Request struct release** -- `handleWorkItem`'s defer releases the pooled body buffer (via `bufpool.PutMsgBuf`) and then hands the request struct back to the `msgcache` bounded chan (Tread / Twrite / Twalk / Tclunk / Tlopen / Tgetattr). Buffer release must precede `putCachedMsg` because `Twrite.Data` aliases the pooled buffer.
- **Shutdown** -- `cleanup()` cancels inflight contexts, waits with a deadline for handlers to drain, closes `workCh` so workers exit, then clunks all remaining fids.

## Performance Workflow

Benchmarks live in `server/*_bench_test.go`. The expected local workflow:

1. **Baseline + candidate** -- run each benchmark twice, once before and once after the change, writing output to files for `benchstat` comparison.

   ```bash
   go test -bench=BenchmarkRoundTrip -benchmem -count=10 -run=^$ ./server/ | tee baseline.txt
   # ... apply change ...
   go test -bench=BenchmarkRoundTrip -benchmem -count=10 -run=^$ ./server/ | tee candidate.txt
   benchstat baseline.txt candidate.txt
   ```

2. **Heap-churn diagnosis** -- `GODEBUG=gctrace=1` prints a line per GC cycle. Runaway churn between iterations of the same bench signals a pool-drain feedback loop (e.g., buffers returned to `sync.Pool` but the pool is drained before the next iteration retrieves them).

   ```bash
   GODEBUG=gctrace=1 go test -bench=BenchmarkRead_ -run=^$ ./server/ 2>&1 | head -40
   ```

3. **Alloc attribution (memprofile)** -- use `-memprofile` to locate allocation sites. In sandboxed execution the default `/tmp` is not writable; write profiles under `/tmp/claude/` instead.

   ```bash
   mkdir -p /tmp/claude
   go test -bench=BenchmarkRead_ -benchmem -memprofile=/tmp/claude/mem.prof -run=^$ ./server/
   go tool pprof -text -alloc_objects -lines /tmp/claude/mem.prof
   ```

4. **Transport choice matters for writev** -- `net.Pipe` does NOT implement `io.ReaderFrom`/`net.Buffers` fast paths, so `net.Buffers.WriteTo` falls back to sequential writes. Benchmarks using `net.Pipe` will miss the writev savings. Use the `unixPair` helper in `writev_bench_test.go` for an honest comparison against `pipePair`.

   ```bash
   go test -bench=BenchmarkWriteApproach -run=^$ ./server/
   ```

5. **CPU profile for hot loops** -- pair with `-cpuprofile=/tmp/claude/cpu.prof` when the `allocs/op` delta is small but `ns/op` regresses.

## Benchmark Helpers

All helpers live under `server/` with `_test.go` suffixes. Reuse them in new benchmarks rather than rolling your own setup.

| Helper | File | Purpose |
|--------|------|---------|
| `newConnPair(tb, root, opts...)` | `conn_test.go` | In-memory `net.Pipe` pair with `ServeConn` running; auto-negotiates `Tversion` at msize 65536 |
| `newConnPairMsize(tb, root, msize, opts...)` | `io_bench_test.go` | Same as above but with a caller-chosen msize; required for benchmarks that negotiate > 64 KiB |
| `mustEncode(tb, tag, msg)` | `bench_test.go` | Pre-encode a wire frame once, outside the hot loop |
| `drainResponse(c)` | `bench_test.go` | Consume one size-prefixed frame from the wire and discard the body |
| `benchAttachFid0(b, cp)` | `bench_test.go` | Wire fid 0 to root before the measurement loop starts |
| `benchWalkOpen(b, cp, fid, newFid, name)` | `io_bench_test.go` | Walk + Tlopen in one call; returns the negotiated `iounit` from Rlopen |
| `treadOffsetPos` / `twriteOffsetPos` | `io_bench_test.go` | Byte offset of the `Offset` field in a pre-encoded Tread/Twrite frame. Patch a new offset with `binary.LittleEndian.PutUint64(frame[treadOffsetPos:], off)` instead of re-encoding per iteration |
| `unixPair(tb)` | `writev_bench_test.go` | Real unix-domain socket pair (supports `writev` via `net.Buffers.WriteTo`) |
| `pipePair(tb)` | `writev_bench_test.go` | `net.Pipe` pair with a drainer goroutine; used as the no-writev control |

### Patching offsets instead of re-encoding

Re-encoding a Tread/Twrite frame inside the measurement loop pollutes allocs/op with encoder noise. Patch the offset bytes in-place:

```go
frame := mustEncode(b, proto.Tag(1), &proto.Tread{Fid: 0, Offset: 0, Count: 4096})
for b.Loop() {
    binary.LittleEndian.PutUint64(frame[treadOffsetPos:], offsets[idx%numOffsets])
    _, _ = cp.client.Write(frame)
    _ = drainResponse(cp.client)
    idx++
}
```

## Go Performance Gotchas

These are the production-relevant footguns the server already pays around. Follow the same pattern in new code.

- **`sync.Pool` cross-P overhead on tiny structs.** Pooling hot request structs (`*proto.Tread` etc.) regressed server-level throughput by ~15% under the goroutine-per-request model: the pool's per-P cache got stolen across Ps faster than callers could reuse an entry. `server/msgcache.go` uses bounded `chan *T` (cap 3) per hot type instead -- non-blocking send/recv, no cross-P balancing, ~1 alloc amortized across many requests. See `BenchmarkMessageAlloc` in `msgalloc_bench_test.go` for the comparison harness.
- **Method-value closures on interface receivers.** Writing `r.Release` where `r` is an interface value allocates a heap closure per request. `conn.handleWorkItem` stores the `releaser` interface value directly on the work item and invokes `release.Release()` at the call site -- virtual dispatch, zero closure alloc.
- **`net.Buffers.WriteTo` consumes its slice.** `net.Buffers.consume` rewrites the slice header, including capacity, as it drains buffers. Rebuilding a slice literal each call (`net.Buffers{hdr, body}`) escapes to the heap; using a conn-resident backing array (`c.encBufsArr [3][]byte`, reassigned under `writeMu` inside `sendResponseInline`) keeps the slice header on the stack.
- **Bucketed `bufpool` for body buffers.** A 7-byte Tclunk should not claim a 1 MiB buffer. `internal/bufpool/bufpool.go` holds four size classes (1 K / 4 K / 64 K / 1 M) backed by one `sync.Pool` each, plus a `PutMsgBuf` cap guard that drops any buffer whose `cap` does not exactly match a bucket so mis-sized entries cannot poison the pool. The pool stores `*[]byte` rather than `[]byte` because the slice header would force a box allocation through `sync.Pool.Put`'s `any` parameter.
- **`sync.Pool` has an embedded `noCopy`.** Returning a `sync.Pool` by value from a factory function trips `go vet`. `msgBufBuckets` is declared as a composite-literal array of `sync.Pool` with each `New` closure hard-coded; the array never moves.
- **Slice header escape on interface conversion.** `newMessage` returns `proto.Message`; the boxed pointer escapes to the heap regardless of pooling. The `msgcache` channels amortize that allocation across many requests.

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

## Pre-Push Checklist

Match what CI runs (`.github/workflows/ci.yml`):

```bash
go vet ./...
go test -race -count=1 ./...
go build -trimpath ./...
golangci-lint run ./...
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
| `bufpool.GetMsgBuf(n)` / `PutMsgBuf(b)` | `internal/bufpool/bufpool.go` | Bucketed pooled `[]byte` (1K/4K/64K/1M); caller must slice to requested length and must not grow beyond `cap` |
| `bufpool.GetBuf()` / `PutBuf(b)` | `internal/bufpool/bufpool.go` | Pooled `*bytes.Buffer` pre-grown to 1 MiB; PutBuf drops oversized buffers |

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

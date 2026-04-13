<!-- generated-by: gsd-doc-writer -->
# Configuration

ninep is a Go library. All configuration is done programmatically through functional options passed to constructors. There are no config files, environment variables, or CLI flags.

## Server Options

Server options are passed to `server.New(root, opts...)` and control connection handling, observability, and attach behavior.

| Option | Signature | Default | Description |
|--------|-----------|---------|-------------|
| `WithMaxMsize` | `WithMaxMsize(msize uint32)` | `131072` (128KB) | Maximum message size accepted during version negotiation. The negotiated msize is `min(client, server)`. Must be at least 256 bytes. |
| `WithMaxInflight` | `WithMaxInflight(n int)` | `64` | Maximum concurrent in-flight requests per connection. Values less than 1 are clamped to 1. Controls the size of the per-connection semaphore and response channel buffer. |
| `WithIdleTimeout` | `WithIdleTimeout(d time.Duration)` | `0` (no timeout) | Per-connection idle timeout. When greater than zero, read and write deadlines are set on the underlying `net.Conn` before each I/O operation. A connection with no activity for the duration is closed. |
| `WithLogger` | `WithLogger(logger *slog.Logger)` | `slog.Default()` with trace correlation | Structured logger for the server. The handler is automatically wrapped with `NewTraceHandler` to inject `trace_id` and `span_id` attributes when an OTel span is active. |
| `WithTracer` | `WithTracer(tp trace.TracerProvider)` | `nil` (no tracing) | OpenTelemetry `TracerProvider`. When set, an OTel middleware is automatically prepended to the middleware chain, producing a span for every 9P operation. If not set, no tracing overhead is incurred. |
| `WithMeter` | `WithMeter(mp metric.MeterProvider)` | `nil` (no metrics) | OpenTelemetry `MeterProvider`. When set, an OTel middleware is automatically prepended to the middleware chain, recording duration, request/response sizes, and active request counts. If not set, no metrics overhead is incurred. |
| `WithMiddleware` | `WithMiddleware(mw ...Middleware)` | none | Appends middleware to the dispatch chain. The first middleware added is outermost (first to execute, last to see the response). Multiple calls append to the existing chain. |
| `WithAnames` | `WithAnames(m map[string]Node)` | `nil` | Maps aname strings to root nodes for vhost-style attach dispatch. When set, `Tattach` uses the aname field to select the root node. An empty aname falls back to the default root passed to `New`. |
| `WithAttacher` | `WithAttacher(a Attacher)` | `nil` | Sets a custom `Attacher` for full-control attach handling. When set, it takes precedence over both the default root node and any aname map configured via `WithAnames`. |

### Usage

```go
srv := server.New(root,
    server.WithMaxMsize(1<<20),       // 1MB max message size
    server.WithMaxInflight(128),      // 128 concurrent requests
    server.WithIdleTimeout(30*time.Second),
    server.WithLogger(slog.New(slog.NewJSONHandler(os.Stderr, nil))),
    server.WithTracer(otel.GetTracerProvider()),
    server.WithMeter(otel.GetMeterProvider()),
)
```

## Attach Configuration

The server resolves the root node for each `Tattach` request using a three-level precedence:

1. **`WithAttacher`** -- If set, the `Attacher.Attach(ctx, uname, aname)` method handles all attach requests. This provides full control over per-user, per-aname root resolution.
2. **`WithAnames`** -- If set and the client provides a non-empty aname, the server looks up the aname in the map. If the aname is not found, `ENOENT` is returned. An empty aname falls back to the default root.
3. **Default root** -- The `root` node passed to `server.New`.

### Attacher Interface

```go
type Attacher interface {
    Attach(ctx context.Context, uname, aname string) (Node, error)
}
```

### Aname Map Example

```go
srv := server.New(defaultRoot,
    server.WithAnames(map[string]server.Node{
        "data":   dataRoot,
        "config": configRoot,
    }),
)
```

## Middleware

Middleware wraps the dispatch handler chain. The `Handler` and `Middleware` types are:

```go
type Handler func(ctx context.Context, tag proto.Tag, msg proto.Message) proto.Message
type Middleware func(next Handler) Handler
```

Middleware is composed in order: the first added is outermost. Multiple `WithMiddleware` calls append to the chain.

### Built-in Middleware

**`NewLoggingMiddleware`** -- Logs each 9P request at `Debug` level with structured attributes (`op`, `duration`, `error`).

```go
srv := server.New(root,
    server.WithMiddleware(server.NewLoggingMiddleware(logger)),
)
```

**OTel middleware** -- Automatically prepended when `WithTracer` or `WithMeter` is set. Not added manually. Produces spans and records metrics for every 9P operation.

## OpenTelemetry Instruments

When a `TracerProvider` or `MeterProvider` is configured, the server creates the following OTel instruments under the scope `github.com/dotwaffle/ninep/server`:

### Spans

Each 9P operation produces a span with `SpanKindServer`. Attributes:

| Attribute | Type | Description |
|-----------|------|-------------|
| `rpc.system.name` | string | Always `"9p"` |
| `rpc.method` | string | Operation type (e.g., `"Tread"`, `"Twalk"`) |
| `ninep.fid` | int64 | Fid from the request (when applicable) |
| `ninep.path` | string | Resolved path for the fid (when available) |
| `ninep.protocol` | string | Negotiated protocol (`"9P2000.L"` or `"9P2000.u"`) |

Error responses set the span status to `codes.Error`.

### Metrics

| Metric | Type | Unit | Description |
|--------|------|------|-------------|
| `ninep.server.duration` | Float64Histogram | `s` | Duration of 9P server operations |
| `ninep.server.request.size` | Int64Counter | `By` | Size of 9P request messages |
| `ninep.server.response.size` | Int64Counter | `By` | Size of 9P response messages |
| `ninep.server.active_requests` | Int64UpDownCounter | | Number of active 9P requests |
| `ninep.server.connections` | Int64UpDownCounter | | Number of active 9P connections |
| `ninep.server.fid.count` | Int64UpDownCounter | | Number of active fids |

## Logging

The server uses `log/slog` for structured logging. The default logger is `slog.Default()` wrapped with `NewTraceHandler`, which adds `trace_id` and `span_id` attributes when an OTel span context is present.

### Custom Logger

Pass a custom logger with `WithLogger`. The handler is automatically wrapped with `NewTraceHandler`:

```go
handler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})
srv := server.New(root, server.WithLogger(slog.New(handler)))
```

### NewTraceHandler

`NewTraceHandler(inner slog.Handler) slog.Handler` wraps any `slog.Handler` to inject OTel trace context. Use it when constructing loggers outside the server that should correlate with 9P spans:

```go
h := server.NewTraceHandler(slog.NewJSONHandler(os.Stderr, nil))
logger := slog.New(h)
```

## Connection Context

Node handlers receive per-connection metadata via context. Retrieve it with `server.ConnFromContext`:

```go
func (n *MyNode) Read(ctx context.Context, offset uint64, count uint32) ([]byte, error) {
    ci := server.ConnFromContext(ctx)
    // ci.Protocol   -- "9P2000.L" or "9P2000.u"
    // ci.Msize      -- Negotiated message size
    // ci.RemoteAddr -- Remote address of the client
    ...
}
```

## Passthrough Filesystem Options

The `server/passthrough` package provides its own functional options for `NewRoot`.

| Option | Signature | Default | Description |
|--------|-----------|---------|-------------|
| `WithUIDMapper` | `WithUIDMapper(m UIDMapper)` | `IdentityMapper()` | Sets a custom UID/GID mapper for bidirectional mapping between 9P protocol UIDs and host OS UIDs. |

### UIDMapper

`UIDMapper` is a struct with two function fields:

```go
type UIDMapper struct {
    ToHost   func(uid, gid uint32) (uint32, uint32)
    FromHost func(uid, gid uint32) (uint32, uint32)
}
```

- `ToHost` maps 9P protocol UIDs to host OS UIDs (used for `Setattr`, `Lchown`).
- `FromHost` maps host OS UIDs to 9P protocol UIDs (used for `Getattr`).

`IdentityMapper()` returns a mapper where both functions return uid/gid unchanged.

```go
root, err := passthrough.NewRoot("/srv/shared",
    passthrough.WithUIDMapper(passthrough.UIDMapper{
        ToHost:   func(uid, gid uint32) (uint32, uint32) { return uid + 1000, gid + 1000 },
        FromHost: func(uid, gid uint32) (uint32, uint32) { return uid - 1000, gid - 1000 },
    }),
)
```

## memfs Builder API

The `server/memfs` package provides a fluent builder for constructing in-memory filesystem trees. Configuration is done at construction time through builder methods on `*MemDir`.

### NewDir

`NewDir(gen *server.QIDGenerator) *MemDir` creates a root directory node. All child nodes created via builder methods share the same QID generator.

### Builder Methods

All builder methods return the parent `*MemDir` for chaining (except `SubDir`, which returns the child).

| Method | Signature | Description |
|--------|-----------|-------------|
| `AddFile` | `AddFile(name string, data []byte) *MemDir` | Creates a `MemFile` child with mode `0o644`. |
| `AddFileWithMode` | `AddFileWithMode(name string, data []byte, mode uint32) *MemDir` | Creates a `MemFile` child with a custom mode. |
| `AddStaticFile` | `AddStaticFile(name string, content string) *MemDir` | Creates a read-only `StaticFile` child with mode `0o444`. |
| `AddDir` | `AddDir(name string) *MemDir` | Creates a `MemDir` child. Returns the parent, not the child. |
| `SubDir` | `SubDir(name string) *MemDir` | Retrieves an existing child directory by name. Panics if not found or not a `*MemDir`. Construction-time use only. |
| `WithDir` | `WithDir(name string, fn func(*MemDir)) *MemDir` | Creates a child directory, calls `fn` for nested construction, returns the parent. |
| `AddSymlink` | `AddSymlink(name string, target string) *MemDir` | Creates a symbolic link child pointing to `target`. |

### Example

```go
gen := &server.QIDGenerator{}
root := memfs.NewDir(gen).
    AddStaticFile("version", "1.0.0").
    AddFile("config.json", configData).
    WithDir("data", func(d *memfs.MemDir) {
        d.AddFile("cache.db", nil)
    }).
    AddSymlink("latest", "data/cache.db")

srv := server.New(root)
```

## Internal Defaults

These values are not configurable but affect server behavior:

| Constant | Value | Location | Description |
|----------|-------|----------|-------------|
| `minMsize` | `256` | `server/conn.go` | Minimum acceptable negotiated msize. Version negotiation fails if the negotiated value is below this. |
| `cleanupDeadline` | `5s` | `server/cleanup.go` | Maximum time to wait for inflight requests to drain during connection cleanup before force-closing. |

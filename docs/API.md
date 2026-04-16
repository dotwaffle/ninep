<!-- generated-by: gsd-doc-writer -->
# API Reference

`github.com/dotwaffle/ninep` -- a Go library for the 9P2000.L and 9P2000.u network filesystem protocols with a capability-based API.

## Package Overview

| Package | Import Path | Purpose |
|---------|-------------|---------|
| `proto` | `github.com/dotwaffle/ninep/proto` | Wire types, message encoding/decoding, errno constants, Payloader interface, ByteCounter |
| `proto/p9l` | `github.com/dotwaffle/ninep/proto/p9l` | 9P2000.L codec (Encode/Decode) |
| `proto/p9u` | `github.com/dotwaffle/ninep/proto/p9u` | 9P2000.u codec (Encode/Decode) |
| `server` | `github.com/dotwaffle/ninep/server` | Server core, capability interfaces, Inode, middleware |
| `server/memfs` | `github.com/dotwaffle/ninep/server/memfs` | In-memory filesystem nodes (MemFile, MemDir, StaticFile) |
| `server/passthrough` | `github.com/dotwaffle/ninep/server/passthrough` | Host OS passthrough filesystem (Linux only) |
| `server/fstest` | `github.com/dotwaffle/ninep/server/fstest` | Protocol-level test harness for filesystem implementations |

---

## Capability Interfaces (`server/node.go`)

The library uses a capability-based pattern inspired by `go-fuse/v2/fs`. Implement only the interfaces your node needs; unimplemented operations return `proto.ENOSYS` via the embedded `*Inode` defaults.

### Core Interfaces

```go
// Node is the minimal interface every filesystem node must implement.
type Node interface {
    QID() proto.QID
}

// InodeEmbedder is the base interface for nodes using Inode tree management.
type InodeEmbedder interface {
    EmbeddedInode() *Inode
}

// QIDer is implemented by nodes that provide their own QID. Takes precedence
// over Inode.QID when resolving a node's QID.
type QIDer interface {
    QID() proto.QID
}
```

### File Operation Interfaces

| Interface | Method | Description |
|-----------|--------|-------------|
| `NodeOpener` | `Open(ctx context.Context, flags uint32) (FileHandle, uint32, error)` | Open the node with given flags |
| `NodeReader` | `Read(ctx context.Context, buf []byte, offset uint64) (int, error)` | Read into caller buffer at offset; caller sizes buf from Tread count |
| `NodeWriter` | `Write(ctx context.Context, data []byte, offset uint64) (uint32, error)` | Write bytes at offset |
| `NodeGetattrer` | `Getattr(ctx context.Context, mask proto.AttrMask) (proto.Attr, error)` | Get file attributes |
| `NodeSetattrer` | `Setattr(ctx context.Context, attr proto.SetAttr) error` | Set file attributes |
| `NodeCloser` | `Close(ctx context.Context) error` | Cleanup on clunk |
| `NodeFsyncer` | `Fsync(ctx context.Context) error` | Flush node-level state to durable storage (bridge prefers `FileSyncer` if present) |

### Directory Operation Interfaces

| Interface | Method | Description |
|-----------|--------|-------------|
| `NodeLookuper` | `Lookup(ctx context.Context, name string) (Node, error)` | Resolve child by name during walk |
| `NodeReaddirer` | `Readdir(ctx context.Context) ([]proto.Dirent, error)` | Return all directory entries (server handles offset tracking and packing) |
| `NodeRawReaddirer` | `RawReaddir(ctx context.Context, buf []byte, offset uint64) (int, error)` | Read raw dirent bytes into caller buffer at offset (node manages offsets) |
| `NodeCreater` | `Create(ctx context.Context, name string, flags uint32, mode proto.FileMode, gid uint32) (Node, FileHandle, uint32, error)` | Create a file |
| `NodeMkdirer` | `Mkdir(ctx context.Context, name string, mode proto.FileMode, gid uint32) (Node, error)` | Create a subdirectory |
| `NodeSymlinker` | `Symlink(ctx context.Context, name, target string, gid uint32) (Node, error)` | Create a symbolic link |
| `NodeLinker` | `Link(ctx context.Context, target Node, name string) error` | Create a hard link |
| `NodeMknoder` | `Mknod(ctx context.Context, name string, mode proto.FileMode, major, minor, gid uint32) (Node, error)` | Create a device node |
| `NodeReadlinker` | `Readlink(ctx context.Context) (string, error)` | Read symlink target |
| `NodeUnlinker` | `Unlink(ctx context.Context, name string, flags uint32) error` | Remove a directory entry |
| `NodeRenamer` | `Rename(ctx context.Context, oldName string, newDir Node, newName string) error` | Rename/move an entry |

### Filesystem-Level Interfaces

| Interface | Method | Description |
|-----------|--------|-------------|
| `NodeStatFSer` | `StatFS(ctx context.Context) (proto.FSStat, error)` | Return filesystem statistics |
| `NodeLocker` | `Lock(...)` / `GetLock(...)` | POSIX byte-range locking (see below) |

```go
type NodeLocker interface {
    Lock(ctx context.Context, lockType proto.LockType, flags proto.LockFlags,
        start, length uint64, procID uint32, clientID string) (proto.LockStatus, error)
    GetLock(ctx context.Context, lockType proto.LockType,
        start, length uint64, procID uint32, clientID string) (proto.LockType, uint64, uint64, uint32, string, error)
}
```

### Extended Attribute Interfaces

| Interface | Method | Description |
|-----------|--------|-------------|
| `NodeXattrGetter` | `GetXattr(ctx context.Context, name string) ([]byte, error)` | Read an extended attribute |
| `NodeXattrSetter` | `SetXattr(ctx context.Context, name string, data []byte, flags uint32) error` | Set an extended attribute |
| `NodeXattrLister` | `ListXattrs(ctx context.Context) ([]string, error)` | List extended attribute names |
| `NodeXattrRemover` | `RemoveXattr(ctx context.Context, name string) error` | Remove an extended attribute |

### Raw Xattr Interface

`RawXattrer` provides protocol-level control over the two-phase xattr flow. When implemented, it takes precedence over the simple xattr interfaces above.

```go
type RawXattrer interface {
    HandleXattrwalk(ctx context.Context, name string) ([]byte, error)
    HandleXattrcreate(ctx context.Context, name string, size uint64, flags uint32) (XattrWriter, error)
}

type XattrWriter interface {
    Write(ctx context.Context, data []byte) (int, error)
    Commit(ctx context.Context) error
}
```

---

## Inode (`server/inode.go`)

`Inode` provides default implementations for all capability interfaces (returning `proto.ENOSYS`) and manages the filesystem tree: parent/child relationships, child lookup, and child enumeration. Embed `*Inode` in your node struct and call `Init` to set up the QID and back-reference.

### Methods

```go
// Init initializes the Inode with a QID and a back-reference to the embedding
// node. If node is nil, the Inode references itself.
func (i *Inode) Init(qid proto.QID, node InodeEmbedder)

// EmbeddedInode returns a pointer to the embedded Inode. Satisfies InodeEmbedder.
func (i *Inode) EmbeddedInode() *Inode

// QID returns the Inode's QID.
func (i *Inode) QID() proto.QID

// Parent returns the parent Inode, or nil if this is the root.
func (i *Inode) Parent() *Inode

// AddChild adds a child inode under the given name.
func (i *Inode) AddChild(name string, child *Inode)

// RemoveChild removes a child by name.
func (i *Inode) RemoveChild(name string)

// Children returns a snapshot copy of the children map.
func (i *Inode) Children() map[string]*Inode
```

All capability interface methods on `*Inode` return `proto.ENOSYS` (or zero values with `proto.ENOSYS`). Override them by implementing the corresponding interface on your embedding struct.

### Example

```go
type MyFile struct {
    server.Inode
    data []byte
}

func (f *MyFile) Read(_ context.Context, buf []byte, offset uint64) (int, error) {
    if offset >= uint64(len(f.data)) {
        return 0, nil
    }
    end := min(offset+uint64(len(buf)), uint64(len(f.data)))
    return copy(buf, f.data[offset:end]), nil
}

// Construct:
gen := &server.QIDGenerator{}
f := &MyFile{data: []byte("hello")}
f.Init(gen.Next(proto.QTFILE), f)
```

---

## FileHandle Interfaces (`server/filehandle.go`)

Per-open state returned by `NodeOpener.Open`. `FileHandle` is an alias for `any`; the server uses type assertions against the File* capability interfaces. When a method exists on both the FileHandle and the Node, the FileHandle path is preferred.

```go
// FileHandle is a marker type for per-open state (alias for any).
type FileHandle any

// FileReader -- per-handle Read. Caller supplies a buf sized to the
// Tread count (clamped to msize); implementation fills and returns n.
type FileReader interface {
    Read(ctx context.Context, buf []byte, offset uint64) (int, error)
}

// FileWriter -- per-handle Write.
type FileWriter interface {
    Write(ctx context.Context, data []byte, offset uint64) (uint32, error)
}

// FileReleaser -- cleanup on clunk.
type FileReleaser interface {
    Release(ctx context.Context) error
}

// FileSyncer -- flush buffered writes on the open handle. Preferred over
// NodeFsyncer when present on the handle.
type FileSyncer interface {
    Fsync(ctx context.Context) error
}

// FileReaddirer -- per-handle directory entry enumeration.
type FileReaddirer interface {
    Readdir(ctx context.Context) ([]proto.Dirent, error)
}

// FileRawReaddirer -- per-handle raw dirent bytes.
type FileRawReaddirer interface {
    RawReaddir(ctx context.Context, buf []byte, offset uint64) (int, error)
}
```

Dispatch priority: `FileHandle` interface -> `Node` interface -> `proto.ENOSYS`. A nil FileHandle is permitted; the server skips FileHandle dispatch and falls through to Node-level capability dispatch.

---

## Composable Base Types (`server/composable.go`)

Convenience types for common patterns. Embed in your struct to get a semantic base type:

```go
// ReadOnlyFile -- Open/Read/Getattr only; Write returns ENOSYS.
type ReadOnlyFile struct { Inode }

// ReadOnlyDir -- Lookup/Readdir/Getattr only; Create/Mkdir/Write return ENOSYS.
type ReadOnlyDir struct { Inode }
```

The compile-time surface is identical to embedding `Inode` directly; the named types document the contract.

---

## QID Utilities (`server/qid.go`)

### QIDGenerator

Produces QIDs with monotonically increasing `Path` values. Safe for concurrent use.

```go
type QIDGenerator struct{ /* atomic counter */ }

// Next returns a new QID with the given type and a unique path.
func (g *QIDGenerator) Next(t proto.QIDType) proto.QID
```

### PathQID

Returns a deterministic QID derived from a path string using FNV-1a 64-bit hashing. Useful for nodes with stable, known paths. FNV-1a is not cryptographic -- unsuitable for hashing untrusted user-supplied path components.

```go
func PathQID(t proto.QIDType, path string) proto.QID
```

---

## Convenience Helpers (`server/helpers.go`)

### SymlinkTo

Creates a symlink node implementing `NodeReadlinker`.

```go
func SymlinkTo(gen *QIDGenerator, target string) *Symlink
```

`Symlink` embeds `Inode`, has a `Target string` field, and implements `Readlink`.

### DeviceNode

Creates a device node with major/minor numbers.

```go
func DeviceNode(gen *QIDGenerator, major, minor uint32) *Device
```

`Device` embeds `Inode` and has `Major`, `Minor uint32` fields.

### StaticStatFS

Creates a node that returns fixed filesystem statistics.

```go
func StaticStatFS(gen *QIDGenerator, stat proto.FSStat) *StaticFS
```

`StaticFS` embeds `Inode`, has a `Stat proto.FSStat` field, and implements `NodeStatFSer`.

---

## Server (`server/server.go`)

### Constructor

```go
// New creates a Server rooted at the given Node. The root must implement
// NodeLookuper for walk resolution.
func New(root Node, opts ...Option) *Server
```

### Serving

```go
// Serve accepts connections from ln and serves each in a goroutine.
// Blocks until context is cancelled or listener returns an error.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error

// ServeConn serves a single 9P connection. Blocks until the connection
// is closed or the context is cancelled. Honors WithMaxConnections limits.
func (s *Server) ServeConn(ctx context.Context, nc net.Conn)
```

---

## Server Options (`server/options.go`)

All options are passed to `server.New(root, opts...)`.

| Option | Signature | Default | Description |
|--------|-----------|---------|-------------|
| `WithMaxMsize` | `func(msize uint32) Option` | `1048576` (1 MiB) | Maximum message size for version negotiation |
| `WithMaxInflight` | `func(n int) Option` | `64` | Max concurrent in-flight requests per connection. Values < 1 clamped to 1 |
| `WithMaxConnections` | `func(n int) Option` | `0` (unlimited) | Max concurrent connections. Over-limit connections closed immediately and counted via `ninep.server.connections_rejected`. Values < 1 disable the limit |
| `WithMaxFids` | `func(n int) Option` | `0` (unlimited) | Max concurrent fids per connection. Over-limit fid-creating ops return `EMFILE`. Values < 1 disable |
| `WithLogger` | `func(logger *slog.Logger) Option` | `slog.Default()` with trace correlation | Structured logger; handler auto-wrapped with `NewTraceHandler` |
| `WithAnames` | `func(m map[string]Node) Option` | `nil` | Vhost-style attach dispatch by aname |
| `WithAttacher` | `func(a Attacher) Option` | `nil` | Custom attach handler; overrides root and aname map |
| `WithIdleTimeout` | `func(d time.Duration) Option` | `0` (disabled) | Per-connection idle timeout |
| `WithMiddleware` | `func(mw ...Middleware) Option` | `nil` | Append middleware to dispatch chain |
| `WithTracer` | `func(tp trace.TracerProvider) Option` | `nil` | OTel tracing; auto-prepends tracing middleware |
| `WithMeter` | `func(mp metric.MeterProvider) Option` | `nil` | OTel metrics; auto-prepends metrics middleware |

### Attacher Interface

```go
type Attacher interface {
    Attach(ctx context.Context, uname, aname string) (Node, error)
}
```

When set via `WithAttacher`, handles all Tattach requests, taking precedence over both the default root node and any aname map.

---

## Middleware (`server/middleware.go`)

### Types

```go
// Handler processes a decoded 9P message and returns the response.
type Handler func(ctx context.Context, tag proto.Tag, msg proto.Message) proto.Message

// Middleware wraps a Handler, adding behavior before and/or after dispatch.
type Middleware func(next Handler) Handler
```

Middleware runs in order: the first added via `WithMiddleware` is outermost (first to execute, last to see the response).

### Example

```go
logging := func(next server.Handler) server.Handler {
    return func(ctx context.Context, tag proto.Tag, msg proto.Message) proto.Message {
        slog.Info("request", "op", msg.Type().String())
        return next(ctx, tag, msg)
    }
}

srv := server.New(root, server.WithMiddleware(logging))
```

---

## OpenTelemetry Integration (`server/otel.go`)

### WithTracer

```go
func WithTracer(tp trace.TracerProvider) Option
```

Produces a span for every 9P operation with attributes:
- `rpc.system.name`: `"9p"`
- `rpc.method`: operation name (e.g., `"Tread"`)
- `ninep.fid`: fid number
- `ninep.path`: resolved file path
- `ninep.protocol`: `"9P2000.L"` or `"9P2000.u"`

Error responses set span status to `codes.Error`.

### WithMeter

```go
func WithMeter(mp metric.MeterProvider) Option
```

Records the following metrics under instrumentation scope `github.com/dotwaffle/ninep/server`:

| Metric | Type | Unit | Description |
|--------|------|------|-------------|
| `ninep.server.duration` | Float64Histogram | `s` | Duration of 9P server operations |
| `ninep.server.request.size` | Int64Counter | `By` | Size of 9P request messages |
| `ninep.server.response.size` | Int64Counter | `By` | Size of 9P response messages |
| `ninep.server.active_requests` | Int64UpDownCounter | -- | Number of active 9P requests |
| `ninep.server.connections` | Int64UpDownCounter | -- | Number of active connections |
| `ninep.server.connections_rejected` | Int64Counter | -- | Connections rejected due to `WithMaxConnections` limit |
| `ninep.server.fid.count` | Int64UpDownCounter | -- | Number of active fids |

Request and response sizes are measured via `proto.ByteCounter` -- a zero-alloc `io.Writer` that sums field widths without materialising a buffer. Both counters guard attribute computation behind `Enabled()` so noop meters skip the counting entirely.

If neither `WithTracer` nor `WithMeter` is set, no tracing or metrics overhead is incurred.

---

## Logging (`server/logging.go`)

### NewTraceHandler

Wraps a `slog.Handler` with OTel trace ID correlation. Log records emitted within an active span context include `trace_id` and `span_id` attributes.

```go
func NewTraceHandler(inner slog.Handler) slog.Handler
```

Applied automatically when using `WithLogger`. Use directly when constructing custom loggers.

### NewLoggingMiddleware

Returns a `Middleware` that logs each 9P request at `slog.LevelDebug` with structured attributes: `op`, `duration`, and `error`.

```go
func NewLoggingMiddleware(logger *slog.Logger) Middleware
```

---

## Context Utilities (`server/context.go`)

### ConnInfo

```go
type ConnInfo struct {
    Protocol   string // "9P2000.L" or "9P2000.u"
    Msize      uint32 // Negotiated message size
    RemoteAddr string // Remote address of the client
}

// ConnFromContext returns the ConnInfo for the current request.
// Returns nil if not called within a connection handler.
func ConnFromContext(ctx context.Context) *ConnInfo
```

Callers MUST NOT mutate a `ConnInfo` returned from `ConnFromContext` -- the same pointer is shared across every request on the same connection. There is no `NewContext` helper: `ConnInfo` is server-injected from negotiated connection state.

---

## Dirent Encoding (`server/dirent.go`)

```go
// EncodeDirents packs dirents into bytes fitting within maxBytes.
// Returns the packed bytes and the number of entries that fit.
func EncodeDirents(dirents []proto.Dirent, maxBytes uint32) ([]byte, int)
```

Wire format per entry: `qid[13] + offset[8] + type[1] + name[s]` (where `name[s]` = `len[2] + name_bytes`). The returned slice is a freshly-allocated copy-out -- safe to retain past the call boundary.

---

## Sentinel Errors (`server/errors.go`)

| Error | Description |
|-------|-------------|
| `ErrFidInUse` | Fid already present in the fid table |
| `ErrFidNotFound` | Fid lookup failed |
| `ErrNotNegotiated` | Message received before version negotiation |
| `ErrMsizeTooSmall` | Client proposed msize too small for useful payload |
| `ErrNotDirectory` | Walk targets a node that does not implement `NodeLookuper` |
| `ErrFidLimitExceeded` | Per-connection fid cap (`WithMaxFids`) reached; mapped to `proto.EMFILE` on the wire |

---

## memfs Package (`server/memfs`)

In-memory filesystem nodes for synthetic file trees.

### MemFile

Read-write in-memory file. Data stored in a `[]byte` protected by `sync.RWMutex`.

```go
type MemFile struct {
    server.Inode
    Data []byte
    Mode uint32 // POSIX bits; defaults to 0o644 if zero
}
```

Implements: `NodeOpener`, `NodeReader`, `NodeWriter`, `NodeGetattrer`, `NodeSetattrer`.

### MemDir

In-memory directory. Serves entries from Inode children, supports dynamic creation.

```go
type MemDir struct {
    server.Inode
    Mode uint32 // POSIX bits; defaults to 0o755 if zero
}
```

Implements: `NodeOpener`, `NodeReaddirer`, `NodeGetattrer`, `NodeCreater`, `NodeMkdirer`, `NodeUnlinker`.

### StaticFile

Read-only in-memory file with string content. Write returns `ENOSYS` (via embedded Inode default).

```go
type StaticFile struct {
    server.Inode
    Content string
    Mode    uint32 // POSIX bits; defaults to 0o444 if zero
}
```

Implements: `NodeOpener`, `NodeReader`, `NodeGetattrer`.

### Builder API (`server/memfs/builder.go`)

Fluent API for constructing file trees without boilerplate.

```go
// NewDir creates a root MemDir for fluent tree construction.
func NewDir(gen *server.QIDGenerator) *MemDir
```

**Builder methods** (all return `*MemDir` for chaining):

| Method | Signature | Description |
|--------|-----------|-------------|
| `AddFile` | `(name string, data []byte) *MemDir` | Add a MemFile child (mode 0o644) |
| `AddFileWithMode` | `(name string, data []byte, mode uint32) *MemDir` | Add a MemFile with custom mode |
| `AddStaticFile` | `(name string, content string) *MemDir` | Add a read-only StaticFile (mode 0o444) |
| `AddDir` | `(name string) *MemDir` | Add a subdirectory (returns parent) |
| `SubDir` | `(name string) *MemDir` | Retrieve existing child dir for further building |
| `WithDir` | `(name string, fn func(*MemDir)) *MemDir` | Create child dir, call fn, return parent |
| `AddSymlink` | `(name string, target string) *MemDir` | Add a symbolic link child |

**Example:**

```go
gen := &server.QIDGenerator{}
root := memfs.NewDir(gen).
    AddFile("config.json", configData).
    AddStaticFile("version", "1.0.0").
    WithDir("data", func(d *memfs.MemDir) {
        d.AddFile("cache.db", nil)
    })

srv := server.New(root, server.WithMaxMsize(65536))
```

---

## passthrough Package (`server/passthrough`)

Host OS passthrough filesystem using `*at` syscalls. Linux only. All operations delegate to the host kernel via file descriptors, preventing path traversal attacks.

### NewRoot

```go
func NewRoot(hostPath string, opts ...Option) (*Root, error)
```

Creates a passthrough filesystem rooted at `hostPath`. The path must be an existing directory.

### Options

| Option | Signature | Description |
|--------|-----------|-------------|
| `WithUIDMapper` | `func(m UIDMapper) Option` | Custom UID/GID mapping (default: `IdentityMapper()`) |

### UIDMapper

```go
type UIDMapper struct {
    ToHost   func(uid, gid uint32) (uint32, uint32) // required, non-nil
    FromHost func(uid, gid uint32) (uint32, uint32) // required, non-nil
}

func IdentityMapper() UIDMapper
```

Both `ToHost` and `FromHost` MUST be non-nil. Passing a `UIDMapper` with either field nil via `WithUIDMapper` panics on the first translation attempt.

### Implemented Interfaces

**Root** implements: `Node`, `InodeEmbedder`, `NodeOpener`, `NodeGetattrer`, `NodeSetattrer`, `NodeCloser`, `NodeStatFSer`.

**Node** implements all of the above plus: `NodeLookuper`, `NodeReaddirer`, `NodeCreater`, `NodeMkdirer`, `NodeSymlinker`, `NodeLinker`, `NodeMknoder`, `NodeReadlinker`, `NodeUnlinker`, `NodeRenamer`, `NodeLocker`, `NodeXattrGetter`, `NodeXattrSetter`, `NodeXattrLister`, `NodeXattrRemover`.

---

## fstest Package (`server/fstest`)

Protocol-level test harness for validating filesystem implementations against the 9P2000.L contract.

### Check

```go
// Check runs every registered test case against root as a subtest.
func Check(t *testing.T, root server.Node)
```

### CheckFactory

```go
// CheckFactory runs every test case, calling newRoot for each case
// to obtain a fresh root node. Use for implementations where the
// server's cleanup closes OS-level resources.
func CheckFactory(t *testing.T, newRoot func(t *testing.T) server.Node)
```

### Expected Tree

Both `Check` and `CheckFactory` require the root to contain:

```
root/
  file.txt       (content: "hello world")
  empty          (content: "")
  sub/
    nested.txt   (content: "nested content")
```

The `ExpectedTree` variable documents this as a `map[string]string`.

### NewTestTree

```go
// NewTestTree constructs the standard test tree for use with Check.
func NewTestTree(gen *server.QIDGenerator) server.Node
```

### Cases

```go
// Cases holds all registered test cases. Callers MUST NOT mutate.
var Cases []TestCase

type TestCase struct {
    Name string
    Run  func(t *testing.T, root server.Node)
}
```

Test cases cover: walk (root, child, deep, nonexistent, clone), read/write (basic, offset, past-EOF), readdir (basic, empty), create/mkdir, getattr (file, dir), error conditions (walk-from-file, read-dir), unlink, and concurrent read.

### Usage

```go
func TestMyFS(t *testing.T) {
    gen := &server.QIDGenerator{}
    root := buildMyTree(gen)
    fstest.Check(t, root)
}

// Or with per-test fresh roots:
func TestPassthrough(t *testing.T) {
    fstest.CheckFactory(t, func(t *testing.T) server.Node {
        root, err := passthrough.NewRoot(t.TempDir())
        if err != nil {
            t.Fatal(err)
        }
        populateTree(t, root)
        return root
    })
}
```

---

## Proto Package (`proto`)

### Key Types

| Type | Description |
|------|-------------|
| `Fid` | `uint32` -- file handle scoped to a connection |
| `Tag` | `uint16` -- request/response correlation identifier |
| `QID` | Server-unique file identifier: `Type QIDType`, `Version uint32`, `Path uint64` |
| `QIDType` | `uint8` -- file type classification |
| `FileMode` | `uint32` -- 9P file permission and type bits |
| `AttrMask` | `uint64` -- attribute selection bitmask for Tgetattr |
| `SetAttrMask` | `uint32` -- attribute selection bitmask for Tsetattr |
| `Attr` | File attributes (mode, uid, gid, size, timestamps, etc.) |
| `SetAttr` | Attributes to set (valid mask + values) |
| `Dirent` | Directory entry: QID, offset, type, name |
| `FSStat` | Filesystem statistics (type, block size, counts, etc.) |
| `Errno` | `uint32` -- Linux errno values on the wire |
| `Message` | Interface: `Type() MessageType`, `EncodeTo(io.Writer) error`, `DecodeFrom(io.Reader) error` |
| `MessageType` | `uint8` -- protocol message type byte; `String()` returns the human-readable name |

### Payloader Interface

`Payloader` is implemented by response messages that carry a large opaque payload (the user data portion of `Rread` and the dirent bytes of `Rreaddir`). The server's write loop detects Payloaders and issues the payload as a separate `net.Buffers` entry, skipping a copy into the pooled body buffer.

```go
type Payloader interface {
    // EncodeFixed writes only the non-payload body (e.g., the 4-byte
    // count prefix for Rread/Rreaddir).
    EncodeFixed(w io.Writer) error

    // Payload returns the bytes that follow the fixed body on the wire.
    // The slice may alias a pooled buffer; the server arranges for
    // release after writev completes.
    Payload() []byte
}
```

Implementations MUST still provide a correct full-message `EncodeTo` for non-server callers (client-side encoders, tests).

### ByteCounter

`ByteCounter` is an `io.Writer` that counts bytes written without materialising a buffer. Used by the OTel middleware to compute wire sizes via `msg.EncodeTo(&c)` without allocation or memcpy cost.

```go
type ByteCounter int

// Write counts len(p) and discards the bytes. Always succeeds.
func (c *ByteCounter) Write(p []byte) (int, error)
```

The `proto.Write*` helpers type-assert `*ByteCounter` and bypass the slice escape that the `io.Writer` interface would otherwise cause -- the counter adds field widths directly with zero allocations.

### QID Type Constants

| Constant | Value | Meaning |
|----------|-------|---------|
| `QTDIR` | `0x80` | Directory |
| `QTAPPEND` | `0x40` | Append-only |
| `QTEXCL` | `0x20` | Exclusive-use |
| `QTMOUNT` | `0x10` | Mounted channel |
| `QTAUTH` | `0x08` | Authentication file |
| `QTTMP` | `0x04` | Temporary |
| `QTSYMLINK` | `0x02` | Symbolic link |
| `QTFILE` | `0x00` | Regular file |

### Dirent Type Constants

Dirent `Type` bytes match Linux `DT_*` values from `<dirent.h>` (the 9P2000.L kernel client passes the byte verbatim to `dir_emit`). Servers MUST use these, not QID type bits.

| Constant | Value | Meaning |
|----------|-------|---------|
| `DT_UNKNOWN` | `0` | Unknown |
| `DT_FIFO` | `1` | Named pipe (FIFO) |
| `DT_CHR` | `2` | Character device |
| `DT_DIR` | `4` | Directory |
| `DT_BLK` | `6` | Block device |
| `DT_REG` | `8` | Regular file |
| `DT_LNK` | `10` | Symbolic link |
| `DT_SOCK` | `12` | Unix-domain socket |

### Sentinel Values

| Name | Value | Purpose |
|------|-------|---------|
| `NoTag` | `0xFFFF` | Tag for Tversion/Rversion |
| `NoFid` | `0xFFFFFFFF` | "No fid" (e.g., afid when auth not needed) |

### Common Errno Constants

The `proto` package defines all Linux errno values (1--133) plus kernel-internal `ENOTSUPP` (524). Common ones:

| Constant | Value | Meaning |
|----------|-------|---------|
| `ENOENT` | 2 | No such file or directory |
| `EIO` | 5 | Input/output error |
| `EBADF` | 9 | Bad file descriptor |
| `EACCES` | 13 | Permission denied |
| `EEXIST` | 17 | File exists |
| `ENOTDIR` | 20 | Not a directory |
| `EINVAL` | 22 | Invalid argument |
| `ENOSPC` | 28 | No space left on device |
| `EMFILE` | 24 | Too many open files (returned when `WithMaxFids` is exceeded) |
| `ENOSYS` | 38 | Function not implemented |

### Wire Encoding Helpers

```go
func WriteUint8(w io.Writer, v uint8) error
func WriteUint16(w io.Writer, v uint16) error
func WriteUint32(w io.Writer, v uint32) error
func WriteUint64(w io.Writer, v uint64) error
func WriteString(w io.Writer, s string) error
func WriteQID(w io.Writer, q QID) error

func ReadUint8(r io.Reader) (uint8, error)
func ReadUint16(r io.Reader) (uint16, error)
func ReadUint32(r io.Reader) (uint32, error)
func ReadUint64(r io.Reader) (uint64, error)
func ReadString(r io.Reader) (string, error)
func ReadQID(r io.Reader) (QID, error)
```

Write* helpers take a zero-alloc fast path when the caller supplies a `*bytes.Buffer` or a `*ByteCounter`.

### Protocol Constants

| Constant | Value | Description |
|----------|-------|-------------|
| `HeaderSize` | `7` | Frame header: size[4] + type[1] + tag[2] |
| `MaxWalkElements` | `16` | Max path elements in Twalk |
| `MaxStringLen` | `65535` | Max 9P string length (uint16 prefix) |
| `QIDSize` | `13` | Wire size of a QID |
| `MaxDataSize` | `16 MiB` (`1 << 24`) | Hard cap on data allocations from wire input |

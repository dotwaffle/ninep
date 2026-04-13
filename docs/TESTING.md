<!-- generated-by: gsd-doc-writer -->
# Testing

This document covers how to run, write, and maintain tests for the ninep library.

## Test framework and setup

ninep uses Go's standard `testing` package exclusively -- no third-party test frameworks or assertion libraries. The only test-scoped external dependency is the OpenTelemetry SDK (`go.opentelemetry.io/otel/sdk`), used in `server/otel_test.go` and `server/logging_test.go` to verify trace and metric instrumentation.

Go 1.26 is required (matching the `go.mod` directive). Install dependencies before running tests:

```bash
go mod download
```

No additional setup, environment variables, or external services are required.

## Running tests

### Full test suite

Run all tests with the race detector enabled:

```bash
go test -race -count=1 ./...
```

This executes tests across all seven packages:

| Package | Description |
|---------|-------------|
| `proto` | Wire types, QID, Errno, encoding helpers |
| `proto/p9l` | 9P2000.L codec encode/decode and round-trip |
| `proto/p9u` | 9P2000.u codec encode/decode and round-trip |
| `server` | Server core, connection, fid table, walk, flush, bridge, middleware, OTel, logging |
| `server/fstest` | Protocol-level conformance harness |
| `server/memfs` | In-memory filesystem implementation |
| `server/passthrough` | Host-backed passthrough filesystem |

### Package-specific tests

Run tests for a single package:

```bash
go test -race ./server/...
go test -race ./proto/p9l/...
go test -race ./server/fstest/...
```

### Running a single test

```bash
go test -race -run TestVersionNegotiation ./server/
go test -race -run TestRoundTrip ./proto/p9l/
```

## The fstest harness

The `server/fstest` package provides a protocol-level conformance harness for validating filesystem implementations against the 9P2000.L contract. Any `server.Node` tree that matches the expected layout can be tested.

### Expected tree layout

The harness expects the root node to contain:

```
root/
  file.txt       (content: "hello world")
  empty           (content: "")
  sub/
    nested.txt    (content: "nested content")
```

This layout is documented in `fstest.ExpectedTree`.

### Using Check and CheckFactory

**`Check(t, root)`** runs all registered test cases against a single shared root node. Suitable for stateless implementations like memfs:

```go
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

**`CheckFactory(t, newRoot)`** calls the factory function for each test case, creating a fresh root node every time. Required for implementations that hold OS-level resources (e.g., passthrough) where server cleanup closes file descriptors:

```go
import (
    "testing"
    "github.com/dotwaffle/ninep/server"
    "github.com/dotwaffle/ninep/server/fstest"
    "github.com/dotwaffle/ninep/server/passthrough"
)

func TestMyPassthrough(t *testing.T) {
    tmp := t.TempDir()
    // Populate tmp with ExpectedTree files...

    fstest.CheckFactory(t, func(_ *testing.T) server.Node {
        root, err := passthrough.NewRoot(tmp)
        if err != nil {
            t.Fatalf("NewRoot: %v", err)
        }
        return root
    })
}
```

### Built-in test tree

The harness also exports `fstest.NewTestTree(gen)` which builds the expected tree using internal test node types, avoiding a dependency on memfs:

```go
var gen server.QIDGenerator
root := fstest.NewTestTree(&gen)
fstest.Check(t, root)
```

### Test case categories

The harness includes 20 test cases organized into categories:

| Category | Cases | What they verify |
|----------|-------|------------------|
| `walk/*` | 5 | Root attach, child walk, deep walk, nonexistent path, fid cloning |
| `read/*` | 3 | Basic read, offset read, read past EOF |
| `write/*` | 1 | Write and read-back verification |
| `readdir/*` | 2 | Directory listing, empty directory |
| `create/*` | 1 | File creation via Tlcreate |
| `mkdir` | 1 | Directory creation via Tmkdir |
| `getattr/*` | 2 | File and directory attribute retrieval |
| `error/*` | 2 | Walk from file (ENOTDIR), read on directory |
| `unlink/*` | 1 | File unlinking via Tunlinkat |
| `concurrent/*` | 1 | Concurrent read correctness |

### Selective execution

Individual cases can be run via Go's `-run` flag or accessed programmatically through the exported `fstest.Cases` slice:

```go
// Run only walk cases.
for _, tc := range fstest.Cases {
    if strings.HasPrefix(tc.Name, "walk/") {
        t.Run(tc.Name, func(t *testing.T) {
            tc.Run(t, root)
        })
    }
}
```

## Fuzz testing

Fuzz tests exist for both codec packages, verifying the round-trip property: any successfully decoded message must re-encode to identical bytes that decode to an identical message.

### Running fuzz tests

Run the 9P2000.L codec fuzzer:

```bash
go test -fuzz=FuzzCodecRoundTrip ./proto/p9l/
```

Run the 9P2000.u codec fuzzer:

```bash
go test -fuzz=FuzzCodecRoundTrip ./proto/p9u/
```

Both fuzzers are named `FuzzCodecRoundTrip`. They seed with valid encoded messages (Tversion, Twalk, Rread, Rlerror, protocol-specific types) and then verify:

1. Decoding fuzzed bytes does not panic (invalid input returns an error, which is fine).
2. If decode succeeds, re-encoding must also succeed.
3. Decoding the re-encoded bytes must produce an identical tag and message.

Run with a time limit:

```bash
go test -fuzz=FuzzCodecRoundTrip -fuzztime=30s ./proto/p9l/
```

Crash inputs are stored in `proto/p9l/testdata/fuzz/` and `proto/p9u/testdata/fuzz/` and are replayed automatically on subsequent `go test` runs.

## Kernel integration tests

The `server/passthrough` package includes integration tests that mount a 9P filesystem via the Linux kernel's v9fs client and perform real filesystem operations through it. These tests are gated behind a build tag.

### Running kernel tests

```bash
go test -tags integration -run TestKernel ./server/passthrough/
```

These tests require:

- Linux with 9P filesystem support (the `9p` and `9pnet` kernel modules).
- Root privileges or `CAP_SYS_ADMIN` (for mounting). The tests attempt `unshare --user --mount` as a fallback for unprivileged users.
- If mounting is not possible, the tests skip gracefully via `t.Skip`.

### Available kernel tests

| Test | What it verifies |
|------|------------------|
| `TestKernelMountReadFile` | Read a file through a v9fs mount |
| `TestKernelMountWriteFile` | Write a file through a v9fs mount and verify on host |
| `TestKernelMountReaddir` | List directory entries through a v9fs mount |
| `TestKernelMountStat` | Stat a file through a v9fs mount (size, permissions) |
| `TestKernelMountCreateFile` | Create a file through a v9fs mount and verify on host |
| `TestKernelMountSkipGracefully` | Verify graceful skip without root; also validates errno propagation (ENOENT) |

The `mountV9FS` helper starts a passthrough server on a Unix socket, mounts it with `mount -t 9p`, and registers cleanup (unmount, server shutdown) via `t.Cleanup`.

## Writing new tests

### Test file naming

Tests use the `*_test.go` convention in the same package as the code under test (white-box testing). The `proto/p9l` and `proto/p9u` codec tests use the `_test` package suffix (black-box testing).

### Table-driven tests

All tests follow the table-driven pattern with `t.Parallel()`:

```go
func TestMyFeature(t *testing.T) {
    t.Parallel()

    tests := []struct {
        name string
        // inputs and expected outputs
    }{
        {name: "case one", /* ... */},
        {name: "case two", /* ... */},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            t.Parallel()
            // test body
        })
    }
}
```

### Protocol-level testing with newConnPair

For tests that need to exercise the full server pipeline (version negotiation, message dispatch, handler execution), use the `newConnPair` helper in `server/walk_test.go`:

```go
func TestMyProtocolFeature(t *testing.T) {
    t.Parallel()

    root := testTree() // or build your own node tree
    cp := newConnPair(t, root)
    defer cp.close(t)

    // Attach to the server (version already negotiated by newConnPair).
    cp.attach(t, 1, 0, "user", "")

    // Walk to a path.
    msg := cp.walk(t, 2, 0, 1, "sub", "file.txt")
    rw, ok := msg.(*proto.Rwalk)
    if !ok {
        t.Fatalf("expected Rwalk, got %T", msg)
    }

    // Check error responses.
    msg = cp.walk(t, 3, 0, 2, "nonexistent")
    isError(t, msg, proto.ENOENT)
}
```

`newConnPair` handles:

- Creating a `Server` with the given root and options.
- Setting up a `net.Pipe()` pair (in-memory, no real sockets).
- Starting `ServeConn` in a background goroutine.
- Negotiating 9P2000.L version.
- Registering cleanup via `t.Cleanup` (cancel context, close connections).

The returned `connPair` provides helper methods: `attach`, `walk`, `clunk`, and access to the raw `client` connection for sending arbitrary messages via `sendMessage` and `readResponse`.

### Raw message helpers

For lower-level control, use the test helpers in `server/conn_test.go`:

- `sendTversion(t, w, msize, version)` -- writes a raw Tversion frame.
- `readRversion(t, r)` -- reads and parses a raw Rversion.
- `sendMessage(t, w, tag, msg)` -- encodes a full 9P2000.L message via `p9l.Encode`.
- `readResponse(t, r)` -- decodes a full response via `p9l.Decode`.
- `isError(t, msg, errno)` -- asserts a message is an `Rlerror` with the expected errno.

### Custom test nodes

Tests commonly define inline node types that embed `server.Inode` and implement only the interfaces needed. Examples from the test suite:

- `rootNode` / `dirNode` / `testDir` -- minimal directory nodes with Inode embedding.
- `testFile` -- minimal file node.
- `bridgeFile` / `bridgeDir` -- nodes implementing Open, Read, Write, Getattr, Create, etc. for bridge integration tests.
- `blockingNode` -- Lookup blocks until a channel is closed or context is cancelled (for flush/cancellation tests).
- `countingNode` -- tracks concurrent active Lookup calls (for max-inflight tests).
- `panicNode` -- panics in Lookup (for panic recovery tests).
- `stuckNode` -- ignores context cancellation (for drain deadline tests).

### Compile-time interface checks

Tests use compile-time assertions to verify nodes implement the expected interfaces:

```go
var (
    _ server.Node          = (*myNode)(nil)
    _ server.InodeEmbedder = (*myNode)(nil)
    _ server.NodeLookuper  = (*myNode)(nil)
)
```

## Race detector

The race detector (`-race` flag) is mandatory for all test runs. The server uses concurrent goroutines extensively:

- Goroutine-per-connection (`ServeConn`).
- Goroutine-per-request (dispatch handlers).
- Single writer goroutine per connection.
- Shared `fidTable` protected by `sync.RWMutex`.
- `inflightMap` for tag tracking and flush cancellation.

Run with race detection:

```bash
go test -race -count=1 ./...
```

The `-count=1` flag disables test caching, ensuring every run exercises the race detector. Several tests specifically target concurrency correctness:

- `TestFidTable_ConcurrentAccess` -- 100 goroutines performing concurrent add/get/clunk on the fid table.
- `TestRapidConnectDisconnect` -- 50 concurrent connect/disconnect cycles checking for goroutine leaks.
- `TestConcurrentDispatch` -- multiple in-flight requests with concurrent handler execution.
- `TestMemFileConcurrent` -- concurrent read/write on in-memory files.

## CI integration

No CI pipeline configuration files (`.github/workflows/`) are present in the repository. The recommended CI test command is:

```bash
go test -race -count=1 ./...
```

For CI environments with Linux kernel support, also run the integration tests:

```bash
go test -race -tags integration ./server/passthrough/
```

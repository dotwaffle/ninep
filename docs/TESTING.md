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

Extended conformance suites also exist alongside `Check` and `CheckFactory`:

- `fstest.CheckLock(t, newRoot)` — exercises Tlock / Tgetlock semantics (`server/fstest/fstest_lock.go`).
- `fstest.CheckXattr(t, newRoot)` — exercises Txattrwalk / Txattrcreate and the two-phase xattr fid lifecycle (`server/fstest/fstest_xattr.go`).

Both take the factory form because locks and xattr fids attach mutable state to the filesystem.

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

### Writing new fstest cases

`fstest.Cases` is a package-level `[]TestCase` populated by `init()` in `server/fstest/cases.go`. A test case is just a `{Name string; Run func(t *testing.T, root server.Node)}`. Cases receive a fully-negotiated `testConn` via the internal `newTestConn` helper — they only need to drive the wire protocol and assert on responses. Preserve the `category/case` naming convention (`walk/root_attach`, `read/offset`) so callers can filter by prefix.

## Fuzz testing

Fuzz tests exist for both codec packages, verifying the round-trip property: any successfully decoded message must re-encode to identical bytes that decode to an identical message. See `proto/p9l/fuzz_test.go` and `proto/p9u/fuzz_test.go`.

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

CI runs both fuzzers for 30 seconds on every push and pull request (`.github/workflows/ci.yml` `fuzz` job).

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

CI compiles the integration tests every build (`go test -tags integration -run ^$ ./...`) but does not execute them — the kernel mount machinery is exercised locally or on dedicated runners.

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

- `rootNode` (`server/conn_test.go`, built via `newRootNode`) -- minimal directory node used across conn/bench tests.
- `testDir` / `testFile` (`server/walk_test.go` via `testTree`) -- minimal directory and file nodes.
- `bridgeFile` / `bridgeDir` (`server/bridge_test.go`) -- nodes implementing Open, Read, Write, Getattr, Create, Mkdir for bridge integration tests.
- `blockingNode` -- Lookup blocks until a channel is closed or context is cancelled (for flush/cancellation tests).
- `countingNode` -- tracks concurrent active Lookup calls (for max-inflight tests).
- `panicNode` -- panics in Lookup (for panic recovery tests).
- `stuckNode` -- ignores context cancellation (for drain deadline tests).

Node capability signatures a new test node must match (current as of v1.1.3+ buf-passing Read API):

```go
// Read fills caller-provided buf; returns bytes written.
func (f *myFile) Read(ctx context.Context, buf []byte, offset uint64) (int, error) { /* ... */ }

// Write accepts data and offset; returns bytes consumed.
func (f *myFile) Write(ctx context.Context, data []byte, offset uint64) (uint32, error) { /* ... */ }
```

The older `Read(ctx, offset, count) ([]byte, error)` shape was removed when Read switched to caller-owned buffers so the server can pool response buffers end-to-end.

### Compile-time interface checks

Tests use compile-time assertions to verify nodes implement the expected interfaces:

```go
var (
    _ server.Node          = (*myNode)(nil)
    _ server.InodeEmbedder = (*myNode)(nil)
    _ server.NodeLookuper  = (*myNode)(nil)
)
```

## Benchmarks

Benchmarks live in `server/` and follow a consistent pattern: `b.ReportAllocs` on every leaf, `b.SetBytes` wherever throughput is meaningful, and key=value subtest names so `benchstat` can group and diff across runs.

### Benchmark files

| File | What it measures |
|------|------------------|
| `server/bench_test.go` | Round-trip dispatch (`BenchmarkRoundTrip`, `BenchmarkRoundTripWithOTel`), readLoop decode (`BenchmarkReadDecode`), fidTable contention, walk+clunk cycles, directory-entry encoding |
| `server/io_bench_test.go` | Tread/Twrite throughput at varying sizes and access patterns (`BenchmarkRead`, `BenchmarkWrite`, `BenchmarkReadPipelined`) against a 128 MiB in-memory file |
| `server/writev_bench_test.go` | Write-path syscall cost: sequential writes vs `net.Buffers.WriteTo` (writev) on unix sockets vs `net.Pipe` (`BenchmarkWriteApproach`) |
| `server/msgalloc_bench_test.go` | Per-request message-struct allocation strategies (heap, `sync.Pool`, stack value) for `BenchmarkMessageAlloc` and `BenchmarkMessageAllocFullDecode` |

### Running benchmarks

```bash
# A single benchmark file or pattern:
go test -bench=BenchmarkRoundTrip -benchmem ./server/
go test -bench=. -benchmem -run ^$ ./server/

# With benchstat-friendly output captured for diffing:
go test -bench=BenchmarkRead -benchmem -count=10 ./server/ > new.txt
benchstat old.txt new.txt
```

The race detector is not used for benchmarks (it distorts throughput and allocation counts).

### Benchmark helpers

All benchmarks build on a small set of helpers from `bench_test.go` and `io_bench_test.go`:

- `newConnPair(tb, root, opts...)` -- server + `net.Pipe` client pair; Tversion already negotiated (`server/walk_test.go`, used by benchmarks via `testing.TB`).
- `newConnPairMsize(tb, root, msize, opts...)` -- same, but with a caller-chosen negotiated msize; required when a benchmark needs msize > 64 KiB (`server/io_bench_test.go`).
- `mustEncode(tb, tag, msg)` -- pre-encode a frame once, outside the measurement loop.
- `drainResponse(c)` -- read and discard exactly one 9P frame from the wire; faster than a full decode.
- `benchAttachFid0(b, cp)` -- wire fid 0 to the server's root before measurement.
- `benchWalkOpen(b, cp, fid, newFid, name)` -- walk + open helper for I/O benchmarks; returns the negotiated IOUnit.
- `treadOffsetPos` / `twriteOffsetPos` constants -- byte offsets for patching the offset field of a pre-encoded Tread/Twrite frame in place (avoids re-encoding per iteration).

Typical skeleton:

```go
func BenchmarkMyOp(b *testing.B) {
    root := newRootNode(proto.QID{Type: proto.QTDIR, Path: 1})
    cp := newConnPair(b, root)
    b.Cleanup(func() { cp.close(b) })

    benchAttachFid0(b, cp)

    frame := mustEncode(b, proto.Tag(1), &p9l.Tgetattr{Fid: 0, RequestMask: proto.AttrAll})
    b.ReportAllocs()
    b.SetBytes(int64(len(frame)))
    for b.Loop() {
        if _, err := cp.client.Write(frame); err != nil {
            b.Fatalf("write: %v", err)
        }
        if err := drainResponse(cp.client); err != nil {
            b.Fatalf("drain: %v", err)
        }
    }
}
```

### Performance workflow

A few workflow notes that are easy to get wrong:

- **`GODEBUG=gctrace=1`** -- prefix a benchmark run with `GODEBUG=gctrace=1` to surface GC activity between iterations. Heap churn mid-benchmark usually points at pool-drain feedback loops or undersized pools, not at the code being measured.
- **Memprofile output path** -- write memprofiles to `/tmp/claude/` rather than `/tmp`; the sandbox this repo is developed in only permits writes under `/tmp/claude/`. Example: `go test -bench=BenchmarkRead -memprofile=/tmp/claude/mem.prof ./server/` then `go tool pprof -text -alloc_objects -lines /tmp/claude/mem.prof`.
- **Transport matters** -- `net.Pipe` does not implement writev, so benchmarks that measure the write path with `net.Pipe` miss the syscall-coalescing savings that real unix and TCP sockets see. Use `writev_bench_test.go`'s `unixPair` helper for realistic numbers on any benchmark where `net.Buffers.WriteTo` is on the hot path; use `pipePair` only as the synthetic A/B baseline.
- **Response flow** -- since v1.1.15 the server writes responses inline from each worker under `writeMu`; there is no `writeLoop` goroutine and no `responses` channel. This means write-path benchmarks should not expect the response to cross a goroutine boundary before being encoded.
- **Request flow** -- since v1.1.10 the server uses a lazily-spawned worker pool bounded by `maxInflight` (not goroutine-per-request). Benchmarks that measure concurrent dispatch should size `WithMaxInflight(...)` explicitly if they depend on specific parallelism.

## Race detector

The race detector (`-race` flag) is mandatory for all test runs. The server uses concurrent goroutines extensively:

- Goroutine-per-connection (`ServeConn`).
- Worker pool: lazily-spawned goroutines (bounded by `maxInflight`) consume decoded requests from `workCh` and run handlers. Workers encode and writev responses inline under `writeMu` (since v1.1.15 there is no separate writer goroutine).
- Shared `fidTable` protected by `sync.RWMutex`.
- `inflightMap` for tag tracking and Tflush cancellation.

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

CI is defined in `.github/workflows/ci.yml` and runs on every push to `main` and every pull request. Three jobs execute in parallel:

| Job | Command | Purpose |
|-----|---------|---------|
| `test` | `go vet ./...`, `go test -race -count=1 ./...`, `go build -trimpath ./...`, `go test -tags integration -run ^$ ./...` | Vet, race-enabled test suite, reproducible build, integration-test compile check |
| `lint` | `golangci-lint run` (via `golangci/golangci-lint-action@v9`, version `latest`) | Static analysis |
| `fuzz` | `go test -fuzz=FuzzCodecRoundTrip -fuzztime=30s ./proto/p9l/` and `./proto/p9u/` | 30s codec fuzz per protocol |

Go version tracks `stable` via `actions/setup-go@v6`. The integration-test step compiles but does not execute the kernel tests (the `-run ^$` pattern matches no tests); actual kernel mount tests run locally or on dedicated runners.

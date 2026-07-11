# API guide

The generated package reference on
[pkg.go.dev](https://pkg.go.dev/github.com/dotwaffle/ninep) is the canonical
source for exported declarations. This guide covers the relationships and
contracts that are easier to understand across packages.

## Package roles

| Package | Role |
|---|---|
| `proto`, `proto/p9l`, `proto/p9u` | Shared wire types and dialect codecs |
| `server` | Connection lifecycle, dispatch, inode tree, and capabilities |
| `server/memfs` | Concurrent in-memory nodes and tree builder |
| `server/passthrough` | Descriptor-anchored Linux and FreeBSD host export |
| `server/fstest` | Protocol-level filesystem conformance tests |
| `client` | Concurrent connection, session, file, and raw protocol APIs |
| `client/clienttest` | In-process server/client test pairs |
| `vsock` | Linux virtio-vsock transport helpers |

## Server model

A node must provide a `QID`. Embedding `server.Inode` supplies the inode tree
and default methods that return `ENOSYS`; the embedding type implements only
the capability interfaces it supports. Call `Inode.Init` with a stable QID and
the embedding node before making the node reachable.

Read and readdir methods receive caller-owned output buffers. File handles
returned by `Open` or `Create` take precedence over equivalent node methods,
which is how implementations attach per-open cursors, locks, and descriptors.

`server.New` validates static configuration and returns `(*Server, error)`.
The defaults bound connection count, fid count, inflight work, negotiation,
drain time, and established I/O. `server.MustNew` is intended for static setup
and tests where invalid options are programmer errors.

## In-memory filesystems

Use `memfs.NewDir` for a root and its fluent builder methods for initial tree
construction. `memfs.NewFile` and `NewFileWithMode` copy their input. A
`MemFile` exposes `Snapshot` and `Replace` for synchronized content access;
mutable storage and metadata are not public fields.

## Passthrough filesystems

`passthrough.NewRoot` opens the export root and uses held descriptors as object
identity. Nodes reject symlink traversal during lookup, and opened directory
handles stream readdir results rather than caching full directory listings.
The package is implemented on Linux and FreeBSD 14 or later.

Device nodes are disabled unless `WithDeviceNodes` is explicitly supplied.
Both callbacks in a custom `UIDMapper` are required and are validated during
root construction.
`WithReadOnly` rejects all mutations. UID/GID mapping does not authorize a
peer, and ownership changes are denied unless `WithOwnershipChanges` is
explicitly supplied.

## Client model

`client.Dial` negotiates a session over an existing `net.Conn`; on success the
returned connection owns that transport. Requests are safe to issue
concurrently. Context-aware methods support cancellation through 9P `Tflush`;
the ordinary `io.Reader` and `io.Writer` methods use the configured request
timeout.

`client.Session` owns redial behavior. Use the raw operation methods when an
application needs protocol messages that do not fit the filesystem-shaped
`File` API.

## Errors

Filesystem errors use `proto.Errno` values and support `errors.Is`. Client
lifecycle and negotiation failures use sentinels from `client`; server
construction returns configuration errors before I/O begins.

## Observability and privacy

Tracing and metrics are disabled unless providers are supplied. Full paths
are omitted from spans by default. `server.WithTracePathFilter` can opt in and
redact, hash, or suppress each path. Request metrics use a bounded `ok` or
`error` outcome attribute; errno detail remains in responses, logs, and spans.

See [Configuration](CONFIGURATION.md) for option defaults and instruments,
and [Getting started](GETTING-STARTED.md) for a runnable example.

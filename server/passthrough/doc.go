// Package passthrough implements a 9P filesystem that proxies all operations
// to the host OS filesystem. It validates the entire ninep library API surface
// and serves as a production-grade reference implementation.
//
// All file operations use *at syscalls relative to directory file descriptors,
// preventing path traversal attacks. UID/GID mapping is configurable with
// identity mapping as the default.
//
// # Platform support
//
// This package supports Linux and FreeBSD. Both ports use the *at syscall
// family for inode-anchored access control. The security model is identical:
// every Node holds an O_PATH fd (O_RDONLY|O_DIRECTORY for directories) rooted
// at a specific inode, and every name-based operation goes through
// (parentFd, name) anchored *at calls.
//
// On FreeBSD/{amd64,arm64,arm,386} the O_PATH flag is sourced from a local
// constant rather than golang.org/x/sys (which doesn't yet export it for
// those architectures); the value matches FreeBSD's <sys/fcntl.h> and is
// architecture-independent on FreeBSD 14+. FreeBSD 14.0+ is the minimum
// supported version.
//
// On other platforms (darwin, windows, etc.) only this package documentation
// compiles and passthrough.NewRoot is undefined. The wider ninep library
// (proto, server, server/memfs, server/fstest, internal/bufpool) builds and
// runs on all platforms supported by Go. To serve 9P from a non-supported
// host, implement your own server.Node types or use server/memfs -- see the
// project README for guidance.
//
// The server process needs appropriate OS permissions for non-identity UID/GID
// mapping (CAP_CHOWN/CAP_FOWNER on Linux; the corresponding privileges on
// FreeBSD).
package passthrough

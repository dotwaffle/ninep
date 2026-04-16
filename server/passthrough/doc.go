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
// This package is Linux-only. It depends on Linux-specific syscalls (O_PATH
// file descriptors, the *at syscall family, getdents64, and the Lgetxattr
// xattr family) for the security model that anchors every Node to a specific
// inode via a held file descriptor. All source files are gated behind
// //go:build linux; on other platforms only this package documentation
// compiles and passthrough.NewRoot is undefined.
//
// The wider ninep library (proto, server, server/memfs, server/fstest,
// internal/bufpool) builds and runs on all platforms supported by Go. To
// serve 9P from a non-Linux host, implement your own server.Node types or
// use server/memfs — see the project README for guidance.
//
// The server process needs appropriate OS permissions for non-identity UID/GID
// mapping (typically CAP_CHOWN and CAP_FOWNER capabilities on Linux).
package passthrough

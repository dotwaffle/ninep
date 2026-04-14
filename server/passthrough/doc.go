// Package passthrough implements a 9P filesystem that proxies all operations
// to the host OS filesystem. It validates the entire ninep library API surface
// and serves as a production-grade reference implementation.
//
// All file operations use *at syscalls relative to directory file descriptors,
// preventing path traversal attacks. UID/GID mapping is configurable with
// identity mapping as the default.
//
// The server process needs appropriate OS permissions for non-identity UID/GID
// mapping (typically CAP_CHOWN and CAP_FOWNER capabilities on Linux).
package passthrough

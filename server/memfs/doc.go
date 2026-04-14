// Package memfs provides in-memory filesystem node types for use with the
// ninep server. MemFile, MemDir, and StaticFile are standalone types that
// embed server.Inode and implement relevant capability interfaces. Use them
// directly or via the fluent builder API (NewDir) to construct synthetic
// file trees without boilerplate.
package memfs

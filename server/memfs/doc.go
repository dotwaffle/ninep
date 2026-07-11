// Package memfs provides in-memory filesystem node types for use with the
// ninep server. MemFile, MemDir, and StaticFile are standalone types that
// embed server.Inode and implement relevant capability interfaces. Use them
// directly or via the builder API rooted at [NewDir] to construct synthetic
// file trees without boilerplate:
//
//	gen := &server.QIDGenerator{}
//	root := memfs.NewDir(gen).
//	    AddStaticFile("hello.txt", "hello world\n").
//	    WithDir("data", func(d *memfs.MemDir) {
//	        d.AddStaticFile("nested.txt", "nested content\n")
//	    })
//	srv, err := server.New(root)
//
// AddStaticFile and WithDir return the receiver for chaining; [MemDir.SubDir]
// returns the child instead, for building a subtree in a local variable.
package memfs

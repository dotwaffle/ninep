package server

// ReadOnlyFile is a composable node for files that support Open, Read,
// and Getattr but not mutation. Embed in your struct and override
// Open/Read/Getattr as needed.
//
// ReadOnlyFile relies on the embedded Inode defaults to return ENOSYS
// for Write (FileWriter), Setattr (NodeSetattrer), Create (NodeCreater),
// Mkdir (NodeMkdirer), Unlink (NodeUnlinker), Rename (NodeRenamer),
// Link (NodeLinker), and Symlink (NodeSymlinker). The type is a
// signal-of-intent: the compile-time surface is identical to embedding
// Inode directly, but the named type documents the contract.
type ReadOnlyFile struct {
	Inode
}

// ReadOnlyDir is a composable node for directories that support Lookup,
// Readdir, and Getattr but not mutation. Embed in your struct and
// override Lookup/Readdir/Getattr as needed.
//
// ReadOnlyDir relies on the embedded Inode defaults to return ENOSYS
// for Create (NodeCreater), Mkdir (NodeMkdirer), Unlink (NodeUnlinker),
// Rename (NodeRenamer), Link (NodeLinker), Symlink (NodeSymlinker), and
// Mknod (NodeMknoder). The type is a signal-of-intent: the compile-time
// surface is identical to embedding Inode directly, but the named type
// documents the contract.
type ReadOnlyDir struct {
	Inode
}

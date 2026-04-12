package server

// ReadOnlyFile is a composable node for files that support Open, Read,
// and Getattr but not Write. Embed in your struct and override
// Open/Read/Getattr as needed. Write operations return ENOSYS via the
// embedded Inode defaults.
type ReadOnlyFile struct {
	Inode
}

// ReadOnlyDir is a composable node for directories that support Lookup,
// Readdir, and Getattr but not Create, Mkdir, or Write. Embed in your
// struct and override Lookup/Readdir/Getattr as needed. Mutating
// operations return ENOSYS via the embedded Inode defaults.
type ReadOnlyDir struct {
	Inode
}

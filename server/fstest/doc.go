// Package fstest provides a protocol-level test harness for validating
// filesystem implementations against the 9P2000.L contract.
//
// Call [Check] with a root Node to run the standard test suite against
// it. The root must contain the following tree shape:
//
//	root/
//	  file.txt  (content: "hello world")
//	  empty     (content: "")
//	  sub/
//	    nested.txt (content: "nested content")
//
// [CheckFactory] is the variant for stateful implementations: it takes a
// newRoot factory and gives every case a fresh root, so cases that
// mutate the tree (create, unlink, rename, setattr) cannot observe each
// other. Two extended suites use the same factory form because their
// state attaches to the filesystem: [CheckLock] exercises
// Tlock/Tgetlock semantics and [CheckXattr] the two-phase
// Txattrwalk/Txattrcreate fid lifecycle.
//
// [Cases] is the exported slice of all standard test cases, enabling
// selective execution via Cases[i].Run(t, root) or filtering by name
// prefix (cases are named category/case, e.g. "walk/root", "read/offset").
package fstest

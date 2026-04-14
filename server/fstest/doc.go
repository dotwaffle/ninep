// Package fstest provides a protocol-level test harness for validating
// filesystem implementations against the 9P2000.L contract.
//
// Call Check(t, root) to run the standard test suite against any root
// Node. The root must contain the following tree shape:
//
//	root/
//	  file.txt  (content: "hello world")
//	  empty     (content: "")
//	  sub/
//	    nested.txt (content: "nested content")
//
// Cases is the exported slice of all test cases, enabling selective
// execution via Cases[i].Run(t, root) or filtering by name prefix.
package fstest

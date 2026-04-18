package client

import (
	"path"
	"strings"
)

// splitPath converts a slash-separated path into the Names slice shape
// expected by Twalk. Uses path.Clean to normalize "." / ".." / "//"
// before splitting on "/" and dropping any empty components.
//
// Returns nil for the root path ("/", "", "."); Twalk with an empty
// Names is the fid-clone operation per the 9P spec and is handled
// explicitly by [File.Clone] -- session methods call splitPath for
// non-root targets only.
//
// Note on ".." handling: path.Clean resolves ".." lexically BEFORE we
// split. This means "/a/../b" yields []string{"b"} -- the ".." never
// reaches the server. Servers that want to allow ".." traversal would
// need to see the raw path components, which this library does not
// send. For 9P's security model this is correct: the attach root is
// the trust boundary, and lexical ".." resolution keeps the client
// from relying on server-side parent-of-root behavior that differs
// across implementations.
func splitPath(p string) []string {
	p = path.Clean(p)
	if p == "." || p == "/" {
		return nil
	}
	p = strings.TrimPrefix(p, "/")
	parts := strings.Split(p, "/")
	out := parts[:0]
	for _, s := range parts {
		if s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

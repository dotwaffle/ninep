// Package clienttest provides test harness helpers that pair a ninep
// server and client over [net.Pipe] for integration tests.
//
// The primary entry point is [Pair], which returns a live
// (*server.Server, *client.Conn) pair with teardown registered via
// [testing.TB.Cleanup]. [MemfsPair] is sugar for the common case where
// the server root is a [memfs] tree — it allocates a fresh
// *server.QIDGenerator, builds an empty *memfs.MemDir, and hands the
// root to a caller-supplied build callback before pairing.
//
// The package mirrors [net/http/httptest] in shape and ergonomics.
// External consumers — projects that depend on ninep and want to build
// integration tests against paired server+client halves without hand-
// wiring net.Pipe, server.New, server.ServeConn, client.Dial, and
// cleanup themselves — import this package directly.
//
// # Stability
//
// The exported surface (Pair, MemfsPair, Option, WithServerOpts,
// WithClientOpts, WithMsize, WithCtx) is part of ninep's public API and
// follows the same semver guarantees as the rest of the module. Test
// harness helpers that leak internal ninep types are explicit design
// decisions; callers relying on specific server/client methods via the
// returned pair are protected by ninep's normal API stability rules.
//
// # Example
//
//	func TestMyFeature(t *testing.T) {
//	    srv, cli := clienttest.MemfsPair(t, func(root *memfs.MemDir) {
//	        root.AddStaticFile("hello.txt", "hello world\n")
//	    })
//	    _ = srv // optional: drive the server side directly.
//
//	    if _, err := cli.Attach(t.Context(), "tester", ""); err != nil {
//	        t.Fatal(err)
//	    }
//	    f, err := cli.OpenFile(t.Context(), "hello.txt", os.O_RDONLY, 0)
//	    if err != nil {
//	        t.Fatal(err)
//	    }
//	    defer f.Close()
//	    // ... read + assert.
//	}
//
// # Msize contract
//
// [WithMsize] sets the proposed msize on BOTH the server
// ([server.WithMaxMsize]) and the client ([client.WithMsize]).
// Asymmetric cases (server cap < client proposal, to exercise negotiation)
// bypass [WithMsize] and use [WithServerOpts]/[WithClientOpts] directly.
package clienttest

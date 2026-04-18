package client_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
	"github.com/dotwaffle/ninep/server/memfs"
)

// readDirTestCtx returns a 5s timeout context for ReadDir tests.
func readDirTestCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), 5*time.Second)
}

// entryNames pulls the Name() off every entry and sorts the slice so
// the assertions are independent of server-side enumeration order.
func entryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// TestFileReadDir_L_SingleCall: on a .L Conn, open the root directory
// and read all entries with ReadDir(-1). The buildTestRoot fixture has
// hello.txt, empty.txt, rw.bin -- expect exactly those three names.
func TestFileReadDir_L_SingleCall(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := readDirTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	dir, err := cli.OpenDir(ctx, "/")
	if err != nil {
		t.Fatalf("OpenDir: %v", err)
	}
	defer func() { _ = dir.Close() }()

	entries, err := dir.ReadDir(-1)
	if err != nil {
		t.Fatalf("ReadDir(-1): %v", err)
	}
	got := entryNames(entries)
	want := []string{"empty.txt", "hello.txt", "rw.bin"}
	if !equalStringSlices(got, want) {
		t.Errorf("ReadDir(-1) names = %v, want %v", got, want)
	}
}

// TestFileReadDir_L_Paginated: ReadDir(2) returns exactly two entries
// on the first call and the remaining entry on the second. A third
// call returns (nil, nil) because the directory is exhausted (io.EOF
// only when explicitly signalled; empty slice is the "exhausted" shape
// since ReadDir has no io.Reader-style short return contract).
func TestFileReadDir_L_Paginated(t *testing.T) {
	t.Parallel()
	cli, cleanup := newClientServerPair(t, buildTestRoot(t))
	defer cleanup()
	ctx, cancel := readDirTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	dir, err := cli.OpenDir(ctx, "/")
	if err != nil {
		t.Fatalf("OpenDir: %v", err)
	}
	defer func() { _ = dir.Close() }()

	first, err := dir.ReadDir(2)
	if err != nil {
		t.Fatalf("ReadDir(2) #1: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("ReadDir(2) #1 len=%d, want 2", len(first))
	}

	second, err := dir.ReadDir(2)
	if err != nil {
		t.Fatalf("ReadDir(2) #2: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("ReadDir(2) #2 len=%d, want 1", len(second))
	}

	// Combined coverage of the three fixture names.
	all := append(first, second...)
	got := entryNames(all)
	want := []string{"empty.txt", "hello.txt", "rw.bin"}
	if !equalStringSlices(got, want) {
		t.Errorf("combined names = %v, want %v", got, want)
	}

	third, err := dir.ReadDir(2)
	if err != nil {
		t.Fatalf("ReadDir(2) #3: err=%v, want nil on exhausted", err)
	}
	if len(third) != 0 {
		t.Errorf("ReadDir(2) #3 len=%d, want 0 (exhausted)", len(third))
	}
}

// TestFileReadDir_EmptyDir: enumerating an empty directory returns an
// empty slice + nil error.
func TestFileReadDir_EmptyDir(t *testing.T) {
	t.Parallel()
	gen := &server.QIDGenerator{}
	root := memfs.NewDir(gen).
		AddDir("empty")
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()
	ctx, cancel := readDirTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	dir, err := cli.OpenDir(ctx, "/empty")
	if err != nil {
		t.Fatalf("OpenDir(/empty): %v", err)
	}
	defer func() { _ = dir.Close() }()

	entries, err := dir.ReadDir(-1)
	if err != nil {
		t.Fatalf("ReadDir(-1): %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("ReadDir(-1) on empty dir: len=%d, want 0", len(entries))
	}
}

// TestFileReadDir_DirEntryInterface: verifies returned entries
// implement the os.DirEntry contract (Name, IsDir, Type, Info) and
// that Info() returns ErrNotSupported per Phase 20's defer to Phase 21
// Tgetattr wiring.
func TestFileReadDir_DirEntryInterface(t *testing.T) {
	t.Parallel()
	gen := &server.QIDGenerator{}
	root := memfs.NewDir(gen).
		AddStaticFile("a.txt", "a").
		AddDir("subdir")
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()
	ctx, cancel := readDirTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	dir, err := cli.OpenDir(ctx, "/")
	if err != nil {
		t.Fatalf("OpenDir: %v", err)
	}
	defer func() { _ = dir.Close() }()

	entries, err := dir.ReadDir(-1)
	if err != nil {
		t.Fatalf("ReadDir(-1): %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("ReadDir(-1) len=%d, want 2", len(entries))
	}

	// Assert: at least one entry has IsDir()==true (subdir) and at
	// least one has IsDir()==false (a.txt). Don't depend on order.
	var (
		sawDir  bool
		sawFile bool
	)
	for _, e := range entries {
		// Compile-time check that entries satisfy os.DirEntry.
		_ = e.Name()
		switch e.Name() {
		case "subdir":
			if !e.IsDir() {
				t.Errorf("subdir.IsDir() = false, want true")
			}
			if e.Type()&fs.ModeDir == 0 {
				t.Errorf("subdir.Type() = %v, want fs.ModeDir bit set", e.Type())
			}
			sawDir = true
		case "a.txt":
			if e.IsDir() {
				t.Errorf("a.txt.IsDir() = true, want false")
			}
			if e.Type() != 0 {
				t.Errorf("a.txt.Type() = %v, want 0 (regular)", e.Type())
			}
			sawFile = true
		}
		// Info() must return ErrNotSupported in Phase 20.
		info, ierr := e.Info()
		if info != nil {
			t.Errorf("Info() = %v, want nil in Phase 20", info)
		}
		if !errors.Is(ierr, client.ErrNotSupported) {
			t.Errorf("Info() err=%v, want ErrNotSupported", ierr)
		}
	}
	if !sawDir || !sawFile {
		t.Errorf("entries did not cover both dir + file: sawDir=%v sawFile=%v", sawDir, sawFile)
	}
}

// TestFileReadDir_MultipleTreaddir: build a directory with enough
// entries that the packed Rreaddir.Data exceeds a single Treaddir's
// count clamp, forcing the client to issue multiple Treaddir round-
// trips as it advances Offset. Each dirent is ~32 bytes on wire, so
// 200 entries at ~40 bytes each ≈ 8000 bytes; with msize=4096 and
// maxChunk ≈ 4072 this forces ≥ 2 Treaddir calls.
func TestFileReadDir_MultipleTreaddir(t *testing.T) {
	t.Parallel()
	gen := &server.QIDGenerator{}
	root := memfs.NewDir(gen)
	const nEntries = 200
	wantNames := make([]string, 0, nEntries)
	for i := 0; i < nEntries; i++ {
		name := randomishName(i)
		root.AddStaticFile(name, "")
		wantNames = append(wantNames, name)
	}
	sort.Strings(wantNames)

	// Small msize forces the chunked loop -- 4096 is below the default
	// 64K but above the min 4K required by the spec.
	cli, cleanup := newClientServerPair(t, root, client.WithMsize(4096))
	defer cleanup()
	ctx, cancel := readDirTestCtx(t)
	defer cancel()

	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	dir, err := cli.OpenDir(ctx, "/")
	if err != nil {
		t.Fatalf("OpenDir: %v", err)
	}
	defer func() { _ = dir.Close() }()

	entries, err := dir.ReadDir(-1)
	if err != nil {
		t.Fatalf("ReadDir(-1): %v", err)
	}
	if len(entries) != nEntries {
		t.Fatalf("ReadDir(-1) len=%d, want %d", len(entries), nEntries)
	}
	got := entryNames(entries)
	if !equalStringSlices(got, wantNames) {
		t.Errorf("ReadDir(-1) names mismatch (len %d vs %d)", len(got), len(wantNames))
	}
}

// randomishName generates a 10-character deterministic name for index i
// so the MultipleTreaddir fixture is reproducible across runs.
func randomishName(i int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	buf := make([]byte, 10)
	n := i
	for j := range buf {
		buf[j] = letters[n%len(letters)]
		n /= len(letters)
		if n == 0 {
			n = i + j + 1
		}
	}
	return string(buf)
}

// equalStringSlices compares two string slices for exact equality.
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// _ keeps proto import live for future dirent assertions.
var _ = proto.DT_DIR
